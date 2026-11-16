package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Read a parent only under an admitted session lease. Admission never revives an
// expired session; a bounded in-flight lease merely protects its existing graph.
func ReadConversationParent(ctx context.Context, owner, session, id string) (*ConversationCheckpoint, error) {
	if DB == nil {
		return nil, errors.New("conversation database unavailable")
	}
	if err := CheckConversationDeletion(ctx, owner, session); err != nil {
		return nil, err
	}
	var live ConversationSession
	if err := stateDB(ctx).Where("id = ? AND owner = ? AND active_until > ?", session, owner, time.Now().Unix()).First(&live).Error; err != nil {
		return nil, err
	}
	var row ConversationCheckpoint
	err := stateDB(ctx).Where("id = ? AND owner = ? AND session_id = ? AND state = ?", id, owner, session, "published").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrConversationMissing
	}
	return &row, err
}

func FindConversationAlias(ctx context.Context, owner, digest string) (*ConversationCheckpoint, error) {
	if DB == nil {
		return nil, errors.New("conversation database unavailable")
	}
	var rows []ConversationCheckpoint
	if err := stateDB(ctx).Where("owner = ? AND upstream_hash = ? AND state = ?", owner, digest, "published").Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	// An upstream ID collision must never guess an account or a branch.
	if len(rows) != 1 {
		return nil, ErrConversationMissing
	}
	return FindConversationCheckpoint(ctx, owner, rows[0].ID)
}

// Recovery never generates again. The financial ledger remains authoritative;
// only a settled matching ledger (or an explicitly free turn) can publish state.
func ReconcileConversationCommits(ctx context.Context, limit int) error {
	if DB == nil {
		return errors.New("conversation database unavailable")
	}
	var receipts []ConversationTurnCommit
	if err := stateDB(ctx).Where("state = ?", "prepared").Order("created_at").Limit(max(1, min(limit, 128))).Find(&receipts).Error; err != nil {
		return err
	}
	for _, receipt := range receipts {
		if receipt.BillingLedgerID != 0 {
			var ledger BillingLedger
			if err := stateDB(ctx).First(&ledger, receipt.BillingLedgerID).Error; err != nil {
				return err
			}
			if ledger.State != BillingLedgerStateSettled || ledger.RequestID != receipt.RequestID || ledger.ActualQuota != receipt.ActualQuota {
				continue
			}
		} else if receipt.ActualQuota != 0 {
			continue
		}
		if err := PublishConversationCommit(ctx, receipt.Owner, receipt.RequestID); err != nil {
			return err
		}
	}
	return nil
}
