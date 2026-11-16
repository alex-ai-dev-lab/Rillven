package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AccountQuotaWindow struct {
	AccountID  string  `gorm:"size:36;primaryKey" json:"account_id"`
	Window     string  `gorm:"size:32;primaryKey" json:"window"`
	Remaining  float64 `gorm:"not null" json:"remaining"`
	ResetAt    int64   `gorm:"not null;index" json:"reset_at"`
	ObservedAt int64   `gorm:"not null" json:"observed_at"`
	Source     string  `gorm:"size:32;not null" json:"source"`
}

type AccountAvailability struct {
	Blocked bool
	Rank    int
}

// After an exhausted window reaches its reset, allow only one generation
// through the shared admission queue until a new observation confirms recovery.
func AccountQuotaHalfOpen(ctx context.Context, subject string) (bool, error) {
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return false, err
	}
	var count int64
	err = stateDB(ctx).Model(&AccountQuotaWindow{}).
		Joins("JOIN upstream_accounts ON upstream_accounts.id = account_quota_windows.account_id").
		Where("upstream_accounts.subject_hash = ? AND account_quota_windows.remaining = 0 AND account_quota_windows.reset_at <= ?", subject, now).Count(&count).Error
	return count > 0, err
}

// Subject hashes are independent of channels and rotating token material.
func AccountAvailabilityBySubject(ctx context.Context, subjects []string) (map[string]AccountAvailability, error) {
	result := make(map[string]AccountAvailability, len(subjects))
	if len(subjects) == 0 {
		return result, nil
	}
	for _, subject := range subjects {
		result[subject] = AccountAvailability{Rank: 100}
	}
	var accounts []UpstreamAccount
	if err := stateDB(ctx).Where("subject_hash IN ?", subjects).Find(&accounts).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(accounts))
	byID := make(map[string]string)
	for _, account := range accounts {
		ids = append(ids, account.ID)
		byID[account.ID] = account.SubjectHash
	}
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return nil, err
	}
	var windows []AccountQuotaWindow
	if len(ids) > 0 {
		if err := stateDB(ctx).Where("account_id IN ? AND reset_at > ?", ids, now).Find(&windows).Error; err != nil {
			return nil, err
		}
	}
	for _, window := range windows {
		subject := byID[window.AccountID]
		value := result[subject]
		if window.Remaining == 0 {
			value.Blocked = true
		}
		if now-window.ObservedAt < int64((2*time.Minute)/time.Millisecond) {
			rank := int((1 - window.Remaining) * 99)
			if value.Rank == 100 || rank > value.Rank {
				value.Rank = rank
			}
		}
		result[subject] = value
	}
	var scopes []UpstreamPoolState
	if err := stateDB(ctx).Where("scope IN ? AND blocked_until > ?", subjects, now).Find(&scopes).Error; err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		value := result[scope.Scope]
		value.Blocked = true
		result[scope.Scope] = value
	}
	var loads []struct {
		Scope   string
		Active  int64
		Waiting int64
	}
	if err := stateDB(ctx).Model(&UpstreamAdmission{}).
		Select("scope, SUM(CASE WHEN active = ? THEN 1 ELSE 0 END) AS active, SUM(CASE WHEN active = ? THEN 1 ELSE 0 END) AS waiting", true, false).
		Where("scope IN ? AND ((active = ? AND expires_at > ?) OR (active = ? AND expires_at > ?))", subjects, true, now-int64(upstreamAdmissionGrace/time.Millisecond), false, now).
		Group("scope").Scan(&loads).Error; err != nil {
		return nil, err
	}
	for _, load := range loads {
		value := result[load.Scope]
		value.Rank += int(min(900, load.Active*128+load.Waiting*4))
		result[load.Scope] = value
	}
	return result, nil
}

func MigrateAccountQuota() error { return DB.AutoMigrate(&AccountQuotaWindow{}, &AccountProbeLease{}) }

func ChannelUsesAccountIdentity(ch *Channel) bool {
	mode := ch.GetSetting().ClientIdentityMode
	if mode == "" || mode == "inherit" {
		// ORM hooks may already hold the only SQLite connection. Read the
		// published option snapshot; the legacy getter may initialize the DB.
		common.OptionMapRWMutex.RLock()
		raw := common.OptionMap["client_identity_setting"]
		common.OptionMapRWMutex.RUnlock()
		var setting struct {
			Enabled bool   `json:"enabled"`
			Scope   string `json:"scope"`
		}
		return common.Unmarshal([]byte(raw), &setting) == nil && setting.Enabled && setting.Scope == "per_account"
	}
	return mode == "per_account"
}
func StoreAccountQuota(ctx context.Context, windows []AccountQuotaWindow) error {
	if len(windows) == 0 {
		return nil
	}
	return retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		for _, w := range windows {
			if w.AccountID == "" || w.Window == "" || math.IsNaN(w.Remaining) || math.IsInf(w.Remaining, 0) || w.Remaining < 0 || w.Remaining > 1 {
				return errors.New("invalid account quota observation")
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&w).Error; err != nil {
				return err
			}
			// An exhausted window cannot be revived by an in-flight success or
			// inconsistent probe. Only an observation from a later reset can do so.
			query := tx.Model(&AccountQuotaWindow{}).Where("account_id = ? AND "+quoteColumn("window")+" = ? AND (observed_at < ? OR (observed_at = ? AND remaining > ?))", w.AccountID, w.Window, w.ObservedAt, w.ObservedAt, w.Remaining)
			if w.Remaining > 0 {
				query = query.Where("remaining > 0 OR reset_at < ?", w.ResetAt)
			}
			if err := query.Updates(map[string]any{"remaining": w.Remaining, "reset_at": w.ResetAt, "observed_at": w.ObservedAt, "source": w.Source}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func quoteColumn(column string) string {
	if DB.Dialector.Name() == "postgres" {
		return "\"" + column + "\""
	}
	return "`" + column + "`"
}

type accountRoutingPolicy struct {
	base     ChannelRoutingPolicy
	eligible map[int]bool
	ranks    map[int]int
}

func (p *accountRoutingPolicy) Empty() bool { return false }
func (p *accountRoutingPolicy) Matches(ch *Channel) bool {
	return p.eligible[ch.Id] && (p.base == nil || p.base.Empty() || p.base.Matches(ch))
}
func (p *accountRoutingPolicy) OrderRank(ch *Channel) int {
	rank := 0
	if p.base != nil && !p.base.Empty() {
		rank = p.base.OrderRank(ch)
	}
	if rank > maxRoutingRank/1024 {
		return maxRoutingRank
	}
	return rank*1024 + p.ranks[ch.Id]
}

// One batched snapshot per selection, rather than a query for each candidate.
// Authorization and configured priority still belong to the existing router.
func prepareAccountRoutingPolicy(channels []*Channel, excluded map[int]bool, base ChannelRoutingPolicy) (ChannelRoutingPolicy, error) {
	ids := make([]int, 0, len(channels))
	pool := make(map[int]bool)
	p := &accountRoutingPolicy{base: base, eligible: make(map[int]bool), ranks: make(map[int]int)}
	for _, ch := range channels {
		if ChannelUsesAccountIdentity(ch) {
			ids = append(ids, ch.Id)
			pool[ch.Id] = true
			p.ranks[ch.Id] = 1000
		}
		p.eligible[ch.Id] = !excluded[ch.Id]
	}
	if len(ids) == 0 {
		return base, nil
	}
	var bindings []ChannelAccountBinding
	queryIDs := append([]int(nil), ids...)
	for id := range excluded {
		if excluded[id] {
			queryIDs = append(queryIDs, id)
		}
	}
	if err := DB.Where("channel_id IN ?", queryIDs).Find(&bindings).Error; err != nil {
		return nil, err
	}
	accountIDs := make([]string, 0, len(bindings))
	excludedAccounts := make(map[string]bool)
	for _, b := range bindings {
		accountIDs = append(accountIDs, b.AccountID)
		if excluded[b.ChannelID] {
			excludedAccounts[b.AccountID] = true
		}
	}
	if len(accountIDs) == 0 {
		for id := range pool {
			p.ranks[id] = 100
		}
		return p, nil
	}
	var accounts []UpstreamAccount
	if err := DB.Where("id IN ?", accountIDs).Find(&accounts).Error; err != nil {
		return nil, err
	}
	scopeIDs := make([]string, 0, len(accounts))
	accountScopes := make(map[string]string)
	for _, a := range accounts {
		scopeIDs = append(scopeIDs, a.SubjectHash)
		accountScopes[a.ID] = a.SubjectHash
	}
	availability, err := AccountAvailabilityBySubject(context.Background(), scopeIDs)
	if err != nil {
		return nil, err
	}
	for id := range pool {
		if len(bindings) > 0 {
			p.eligible[id] = false
		}
	}
	for _, b := range bindings {
		if !pool[b.ChannelID] {
			continue
		}
		value := availability[accountScopes[b.AccountID]]
		if excluded[b.ChannelID] || excludedAccounts[b.AccountID] || value.Blocked {
			continue
		}
		p.eligible[b.ChannelID] = true
		if value.Rank < p.ranks[b.ChannelID] {
			p.ranks[b.ChannelID] = value.Rank
		}
	}
	// Channels with no registered subject may provision on their first request.
	registered := make(map[int]bool)
	for _, b := range bindings {
		registered[b.ChannelID] = true
	}
	for id := range pool {
		if !registered[id] && !excluded[id] {
			p.eligible[id] = true
			p.ranks[id] = 100
		}
	}
	// Duplicate single-account channels cannot multiply that account's lottery
	// weight. Preserve distinct priority/provider buckets and multi-key pools.
	byID := make(map[int]*Channel, len(channels))
	for _, ch := range channels {
		byID[ch.Id] = ch
	}
	representatives := make(map[string]int)
	for _, b := range bindings {
		ch := byID[b.ChannelID]
		if ch == nil || ch.ChannelInfo.IsMultiKey || !p.Matches(ch) {
			continue
		}
		rank := 0
		if base != nil && !base.Empty() {
			rank = base.OrderRank(ch)
		}
		key := fmt.Sprintf("%s/%d/%d", b.AccountID, ch.GetPriority(), rank)
		if id, exists := representatives[key]; exists {
			if ch.Id < id {
				p.eligible[id], representatives[key] = false, ch.Id
			} else {
				p.eligible[ch.Id] = false
			}
		} else {
			representatives[key] = ch.Id
		}
	}
	return p, nil
}
