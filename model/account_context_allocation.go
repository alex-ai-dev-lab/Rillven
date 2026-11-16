package model

import (
	"context"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// All scope keys are keyed digests. The seed is derived from the encrypted
// account seed; database backups retain both the allocation and sequence.
type AccountContextSequence struct {
	ID          string `gorm:"size:64;primaryKey"`
	AccountID   string `gorm:"size:36;not null;index"`
	OwnerHash   string `gorm:"size:64;not null;index"`
	Version     int    `gorm:"not null"`
	NextOrdinal int64  `gorm:"not null"`
}

type AccountContextAllocation struct {
	Digest     string `gorm:"size:64;primaryKey"`
	SequenceID string `gorm:"size:64;not null;uniqueIndex:idx_context_allocation,priority:1"`
	Ordinal    int64  `gorm:"not null;uniqueIndex:idx_context_allocation,priority:2"`
	Value      uint64 `gorm:"not null"`
}

func ResolveAccountContextAllocation(ctx context.Context, account, owner, sequenceID, digest string, seed uint64) (common.ModelFacingID, error) {
	var result common.ModelFacingID
	if account == "" || owner == "" || len(sequenceID) != 64 || len(digest) != 64 {
		return result, errors.New("invalid context allocation scope")
	}
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		sequence := AccountContextSequence{ID: sequenceID, AccountID: account, OwnerHash: owner, Version: common.ModelFacingIDVersion}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&sequence).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where("id = ?", sequenceID).First(&sequence).Error; err != nil {
			return err
		}
		if sequence.AccountID != account || sequence.OwnerHash != owner || sequence.Version != common.ModelFacingIDVersion {
			return errors.New("unsupported context allocation sequence")
		}
		var allocation AccountContextAllocation
		err := tx.Where("digest = ?", digest).First(&allocation).Error
		if err == nil {
			if allocation.SequenceID != sequenceID {
				return errors.New("context allocation scope mismatch")
			}
			result = common.ModelFacingID{Salt: "window", Ordinal: uint64(allocation.Ordinal), Value: allocation.Value}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if sequence.NextOrdinal < 0 || sequence.NextOrdinal == math.MaxInt64 {
			return errors.New("context allocation sequence exhausted")
		}
		result, err = common.ModelFacingIDAt(seed, "window", uint64(sequence.NextOrdinal))
		if err != nil {
			return err
		}
		allocation = AccountContextAllocation{Digest: digest, SequenceID: sequenceID, Ordinal: sequence.NextOrdinal, Value: result.Value}
		if err := tx.Create(&allocation).Error; err != nil {
			return err
		}
		update := tx.Model(&AccountContextSequence{}).Where("id = ? AND next_ordinal = ?", sequenceID, sequence.NextOrdinal).UpdateColumn("next_ordinal", sequence.NextOrdinal+1)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("context allocation position changed")
		}
		return nil
	})
	return result, err
}
