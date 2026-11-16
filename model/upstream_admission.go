package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Scope is a one-way digest of the upstream account, model or proxy boundary.
// The same account reused by multiple channels shares its limits.
type UpstreamPoolState struct {
	Scope        string `gorm:"size:64;primaryKey"`
	BlockedUntil int64  `gorm:"not null;default:0;index"`
	UpdatedAt    int64  `gorm:"not null"`
}

type UpstreamAdmission struct {
	ID        uint64 `gorm:"primaryKey"`
	Scope     string `gorm:"size:64;not null;index"`
	Owner     string `gorm:"size:36;not null;uniqueIndex"`
	Active    bool   `gorm:"not null;default:false"`
	ExpiresAt int64  `gorm:"not null;index"`
}

var ErrUpstreamAdmissionBusy = errors.New("upstream admission unavailable")

// A lost lease cancels the local transport, but the remote server may still be
// finishing it. Keep that capacity quarantined before admitting replacements.
const upstreamAdmissionGrace = 2 * time.Minute

func MigrateUpstreamAdmission() error {
	return DB.AutoMigrate(&UpstreamPoolState{}, &UpstreamAdmission{})
}

func UpstreamBlockedUntil(ctx context.Context, scopes []string) (int64, error) {
	if DB == nil {
		return 0, errors.New("upstream coordination database unavailable")
	}
	var rows []UpstreamPoolState
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return 0, err
	}
	if err := stateDB(ctx).Where("scope IN ? AND blocked_until > ?", scopes, now).Find(&rows).Error; err != nil {
		return 0, err
	}
	var until int64
	for _, row := range rows {
		until = max(until, row.BlockedUntil)
	}
	return until, nil
}

func BlockUpstreamScope(ctx context.Context, scope string, until int64) error {
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return err
	}
	until = min(until, now+int64((24*time.Hour)/time.Millisecond))
	if until <= now {
		return nil
	}
	row := UpstreamPoolState{Scope: scope, UpdatedAt: now}
	return retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		return tx.Model(&row).Updates(map[string]any{"blocked_until": gorm.Expr("CASE WHEN blocked_until < ? THEN ? ELSE blocked_until END", until, until), "updated_at": now}).Error
	})
}

func QueueUpstreamAdmission(ctx context.Context, scope, owner string, queueLimit int, wait time.Duration) (*UpstreamAdmission, error) {
	row := &UpstreamAdmission{}
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		now, err := databaseMillis(tx)
		if err != nil {
			return err
		}
		pool := UpstreamPoolState{Scope: scope, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&pool).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&pool, "scope = ?", scope).Error; err != nil {
			return err
		}
		now, err = databaseMillis(tx)
		if err != nil {
			return err
		}
		if err := tx.Where("scope = ? AND ((active = ? AND expires_at <= ?) OR expires_at <= ?)", scope, false, now, now-int64(upstreamAdmissionGrace/time.Millisecond)).Delete(&UpstreamAdmission{}).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&UpstreamAdmission{}).Where("scope = ? AND active = ?", scope, false).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(queueLimit) {
			return ErrUpstreamAdmissionBusy
		}
		*row = UpstreamAdmission{Scope: scope, Owner: owner, ExpiresAt: now + int64((wait+5*time.Second)/time.Millisecond)}
		return tx.Create(row).Error
	})
	return row, err
}

func TryActivateUpstreamAdmission(ctx context.Context, row *UpstreamAdmission, concurrency int, lease time.Duration) (bool, error) {
	active := false
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		var pool UpstreamPoolState
		if err := lockForUpdate(tx).First(&pool, "scope = ?", row.Scope).Error; err != nil {
			return err
		}
		now, err := databaseMillis(tx)
		if err != nil {
			return err
		}
		if pool.BlockedUntil > now {
			return ErrUpstreamAdmissionBusy
		}
		var ahead int64
		if err := tx.Model(&UpstreamAdmission{}).Where("scope = ? AND ((active = ? AND expires_at > ?) OR (active = ? AND expires_at > ? AND id < ?))", row.Scope, true, now-int64(upstreamAdmissionGrace/time.Millisecond), false, now, row.ID).Count(&ahead).Error; err != nil {
			return err
		}
		if ahead >= int64(concurrency) {
			return nil
		}
		result := tx.Model(&UpstreamAdmission{}).Where("id = ? AND owner = ? AND active = ? AND expires_at > ?", row.ID, row.Owner, false, now).Updates(map[string]any{"active": true, "expires_at": now + int64(lease/time.Millisecond)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUpstreamAdmissionBusy
		}
		active = true
		return nil
	})
	return active, err
}

func RenewUpstreamAdmission(ctx context.Context, row *UpstreamAdmission, lease time.Duration) error {
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return err
	}
	result := stateDB(ctx).Model(&UpstreamAdmission{}).Where("id = ? AND owner = ? AND active = ? AND expires_at > ?", row.ID, row.Owner, true, now).Update("expires_at", now+int64(lease/time.Millisecond))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrUpstreamAdmissionBusy
	}
	return nil
}

func ReleaseUpstreamAdmission(ctx context.Context, row *UpstreamAdmission) error {
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return err
	}
	return stateDB(ctx).Where("id = ? AND owner = ? AND (active = ? OR expires_at > ?)", row.ID, row.Owner, false, now).Delete(&UpstreamAdmission{}).Error
}

func CleanupUpstreamAdmissions(ctx context.Context) error {
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return err
	}
	return stateDB(ctx).Where("(active = ? AND expires_at <= ?) OR expires_at <= ?", false, now, now-int64(upstreamAdmissionGrace/time.Millisecond)).Delete(&UpstreamAdmission{}).Error
}
