package model

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only keyed digests are stored, never caller identifiers or conversation text.
// UUIDv7 allocations are persisted before dispatch and survive replica handoff.
type AccountCorrelation struct {
	Digest    string `gorm:"size:64;primaryKey" json:"-"`
	AccountID string `gorm:"size:36;not null;index" json:"-"`
	OwnerHash string `gorm:"size:64;not null;index" json:"-"`
	ID        string `gorm:"size:36;not null;uniqueIndex" json:"-"`
}

func ResolveAccountCorrelation(ctx context.Context, account, owner, digest string) (string, error) {
	if account == "" || owner == "" || len(digest) != 64 {
		return "", errors.New("invalid account correlation scope")
	}
	var row AccountCorrelation
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		row = AccountCorrelation{}
		err := tx.Where("digest = ?", digest).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			id, idErr := uuid.NewV7()
			if idErr != nil {
				return idErr
			}
			candidate := AccountCorrelation{Digest: digest, AccountID: account, OwnerHash: owner, ID: id.String()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
				return err
			}
			err = lockForUpdate(tx).Where("digest = ?", digest).First(&row).Error
		}
		if err != nil {
			return err
		}
		if row.AccountID != account || row.OwnerHash != owner {
			return errors.New("account correlation scope mismatch")
		}
		return nil
	})
	return row.ID, err
}
