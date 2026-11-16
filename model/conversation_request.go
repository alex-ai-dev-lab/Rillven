package model

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrConversationRequestConflict = errors.New("idempotency key was used for different request content")

// Retain claims after deletion/expiry as tombstones. An unconfirmed result must
// never authorize a second generation, tool execution or billing operation.
type ConversationRequest struct {
	ID          string `gorm:"size:64;primaryKey"`
	Owner       string `gorm:"size:64;not null;index"`
	PayloadHash string `gorm:"size:64;not null"`
	ResponseID  string `gorm:"size:64;not null"`
}

func ClaimConversationRequest(ctx context.Context, owner, id, hash, prefix string) (*ConversationRequest, bool, error) {
	var row ConversationRequest
	created := false
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		candidate := ConversationRequest{ID: id, Owner: owner, PayloadHash: hash, ResponseID: prefix + uuid.NewString()}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		if err := tx.Where("id = ? AND owner = ?", id, owner).First(&row).Error; err != nil {
			return err
		}
		if row.PayloadHash != hash {
			return ErrConversationRequestConflict
		}
		return nil
	})
	return &row, created, err
}
