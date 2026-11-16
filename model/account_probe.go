package model

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AccountProbeLease struct {
	AccountID   string `gorm:"size:36;primaryKey"`
	Owner       string `gorm:"size:36;not null"`
	NextProbeAt int64  `gorm:"not null"`
}

func ClaimAccountProbe(ctx context.Context, account, owner string, interval time.Duration) (bool, error) {
	claimed := false
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		now, err := databaseMillis(tx)
		if err != nil {
			return err
		}
		row := AccountProbeLease{AccountID: account, Owner: owner, NextProbeAt: 0}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		result := tx.Model(&AccountProbeLease{}).Where("account_id = ? AND next_probe_at <= ?", account, now).Updates(map[string]any{"owner": owner, "next_probe_at": now + int64(interval/time.Millisecond)})
		if result.Error != nil {
			return result.Error
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	return claimed, err
}
