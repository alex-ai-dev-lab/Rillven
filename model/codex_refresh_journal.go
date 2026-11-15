package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var ErrRefreshGrantUnavailable = errors.New("OAuth refresh is pending or its result is unknown; reauthorize if it cannot be recovered")

// The grant, rather than the channel, owns refresh. Copies of one rotating grant
// share the receipt; no lease expiry can authorize resending a consumed grant.
type CodexRefreshJournal struct {
	GrantHash  string `gorm:"size:64;primaryKey"`
	LeaseOwner string `gorm:"size:36;not null"`
	LeaseUntil int64  `gorm:"not null"`
	State      string `gorm:"size:16;not null;index"`
	Ciphertext StateBlob
	UpdatedAt  int64 `gorm:"not null;index"`
}

func ClaimCodexRefresh(ctx context.Context, grant, owner string) (*CodexRefreshJournal, error) {
	if DB == nil {
		return nil, errors.New("OAuth journal database unavailable")
	}
	var row CodexRefreshJournal
	err := stateDB(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		initial := CodexRefreshJournal{GrantHash: grant, LeaseOwner: owner, LeaseUntil: now + 45, State: "claimed", UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where("grant_hash = ?", grant).First(&row).Error; err != nil {
			return err
		}
		if row.State == "received" {
			return nil
		}
		if row.State != "claimed" || row.LeaseOwner != owner && row.LeaseUntil > now {
			return ErrRefreshGrantUnavailable
		}
		row.LeaseOwner, row.LeaseUntil, row.UpdatedAt = owner, now+45, now
		return tx.Save(&row).Error
	})
	return &row, err
}

func MarkCodexRefreshSent(ctx context.Context, grant, owner string) error {
	result := stateDB(ctx).Model(&CodexRefreshJournal{}).Where("grant_hash = ? AND lease_owner = ? AND state = ? AND lease_until > ?", grant, owner, "claimed", time.Now().Unix()).Updates(map[string]any{"state": "sent", "updated_at": time.Now().Unix()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRefreshGrantUnavailable
	}
	return nil
}

func RecordCodexRefreshResult(ctx context.Context, grant, owner, state, ciphertext string) error {
	if state != "received" && state != "unknown" && state != "needs_login" {
		return errors.New("invalid OAuth journal state")
	}
	result := stateDB(ctx).Model(&CodexRefreshJournal{}).Where("grant_hash = ? AND lease_owner = ? AND state = ?", grant, owner, "sent").Updates(map[string]any{"state": state, "ciphertext": ciphertext, "updated_at": time.Now().Unix()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRefreshGrantUnavailable
	}
	return nil
}

func ApplyCodexCredentialCAS(ctx context.Context, id int, prior, next string, configVersion ...int64) error {
	// Never put rotating credentials into SQL diagnostics, even on DB errors.
	if DB == nil {
		return errors.New("OAuth journal database unavailable")
	}
	query := stateDB(ctx).Session(&gorm.Session{Logger: logger.Discard}).Model(&Channel{}).
		Where("id = ? AND status IN ?", id, []int{common.ChannelStatusEnabled, common.ChannelStatusAutoDisabled}).
		Where(clause.Eq{Column: "key", Value: prior})
	if len(configVersion) > 0 {
		query = query.Where("config_version = ?", configVersion[0])
	}
	result := query.Update("key", next)
	if result.Error != nil {
		return errors.New("could not persist refreshed credential")
	}
	if result.RowsAffected != 1 {
		return errors.New("channel credential or status changed while refreshing")
	}
	return nil
}

func ReleaseUnsentCodexRefresh(ctx context.Context, grant, owner string) error {
	return stateDB(ctx).Where("grant_hash = ? AND lease_owner = ? AND state = ?", grant, owner, "claimed").Delete(&CodexRefreshJournal{}).Error
}

// Keep grant tombstones: deleting them would authorize replay of consumed grants.
// Only the encrypted rotation receipt expires after its recovery window.
func CompactCodexRefreshReceipts(ctx context.Context) error {
	if DB == nil {
		return errors.New("OAuth journal database unavailable")
	}
	return stateDB(ctx).Model(&CodexRefreshJournal{}).
		Where("state = ? AND updated_at < ?", "received", time.Now().Add(-7*24*time.Hour).Unix()).
		Updates(map[string]any{"state": "expired", "ciphertext": ""}).Error
}
