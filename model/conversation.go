package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrConversationMissing = errors.New("conversation context unavailable")
	ErrConversationBusy    = errors.New("conversation commit pending")
	ErrConversationLimit   = errors.New("conversation storage limit exceeded")
)

type ConversationSession struct {
	ID          string `gorm:"size:48;primaryKey"`
	Owner       string `gorm:"size:64;not null;index"`
	Bytes       int64  `gorm:"not null"`
	Revision    int64  `gorm:"not null"`
	ExpiresAt   int64  `gorm:"index;not null"`
	DeadlineAt  int64  `gorm:"not null"`
	CreatedAt   int64  `gorm:"not null"`
	ActiveUntil int64  `gorm:"not null;default:0"`
}

type ConversationOwnerUsage struct {
	Owner string `gorm:"size:64;primaryKey"`
	Bytes int64  `gorm:"not null"`
}

// Response nodes are immutable. Two continuations of the same parent produce
// separate nodes, so parallel branches cannot overwrite each other's head.
type ConversationCheckpoint struct {
	ID           string    `gorm:"size:64;primaryKey"`
	Owner        string    `gorm:"size:64;not null;index:idx_context_owner_upstream"`
	SessionID    string    `gorm:"size:48;not null;index"`
	ParentID     string    `gorm:"size:64;index"`
	UpstreamID   string    `gorm:"size:256"`
	UpstreamHash string    `gorm:"size:64;index:idx_context_owner_upstream"`
	ChannelID    int       `gorm:"not null"`
	SlotID       string    `gorm:"size:64;not null"`
	RouteKey     string    `gorm:"size:64;not null"`
	State        string    `gorm:"size:16;not null"` // prepared, published
	Ciphertext   StateBlob `gorm:"not null"`
	ContentHash  string    `gorm:"size:64;not null"`
	Bytes        int64     `gorm:"not null"`
	Depth        int       `gorm:"not null"`
	Portable     bool      `gorm:"not null"`
	Shadow       bool      `gorm:"not null;default:false"`
	NativeState  bool      `gorm:"not null;default:false"`
	CreatedAt    int64     `gorm:"not null"`
}

type ConversationResource struct {
	Owner        string `gorm:"size:64;primaryKey"`
	Kind         string `gorm:"size:16;primaryKey"`
	Digest       string `gorm:"size:64;primaryKey"`
	CheckpointID string `gorm:"size:64;not null"`
	SessionID    string `gorm:"size:48;not null;index"`
}

type ConversationTurnCommit struct {
	RequestID       string `gorm:"size:64;primaryKey"`
	Owner           string `gorm:"size:64;not null"`
	SessionID       string `gorm:"size:48;not null;index"`
	CheckpointID    string `gorm:"size:64;not null;uniqueIndex"`
	ChannelID       int    `gorm:"not null"`
	ActualQuota     int64  `gorm:"not null"`
	BillingLedgerID uint64 `gorm:"not null;default:0"`
	State           string `gorm:"size:16;not null;index"` // prepared, published, delivered
	CreatedAt       int64  `gorm:"not null;index"`
}

func MigrateConversationState() error {
	if DB == nil {
		return errors.New("conversation database unavailable")
	}
	return DB.AutoMigrate(&ConversationSession{}, &ConversationOwnerUsage{}, &ConversationCheckpoint{}, &ConversationResource{}, &ConversationTurnCommit{})
}

func FindConversationCheckpoint(ctx context.Context, owner, id string) (*ConversationCheckpoint, error) {
	if DB == nil {
		return nil, errors.New("conversation database unavailable")
	}
	var row ConversationCheckpoint
	err := stateDB(ctx).Where("owner = ? AND id = ?", owner, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrConversationMissing
	}
	if err != nil {
		return nil, err
	}
	if _, err := LoadConversationSession(ctx, owner, row.SessionID); err != nil {
		return nil, err
	}
	if row.State != "published" {
		return nil, ErrConversationBusy
	}
	return &row, nil
}

func FindConversationResource(ctx context.Context, owner, kind, digest string) (*ConversationCheckpoint, error) {
	if DB == nil {
		return nil, errors.New("conversation database unavailable")
	}
	var row ConversationResource
	err := stateDB(ctx).Where("owner = ? AND kind = ? AND digest = ?", owner, kind, digest).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrConversationMissing
	}
	if err != nil {
		return nil, err
	}
	return FindConversationCheckpoint(ctx, owner, row.CheckpointID)
}

func LoadConversationSession(ctx context.Context, owner, id string) (*ConversationSession, error) {
	if DB == nil {
		return nil, errors.New("conversation database unavailable")
	}
	var row ConversationSession
	err := stateDB(ctx).Where("owner = ? AND id = ?", owner, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrConversationMissing
	}
	if err != nil {
		return nil, err
	}
	if row.ExpiresAt <= time.Now().Unix() || row.DeadlineAt <= time.Now().Unix() {
		return nil, ErrConversationMissing
	}
	return &row, nil
}

func AdmitConversationSession(ctx context.Context, owner, id string, idleTTL, turnTTL int64) error {
	now := time.Now().Unix()
	row, err := LoadConversationSession(ctx, owner, id)
	if err != nil {
		return err
	}
	result := stateDB(ctx).Model(&ConversationSession{}).Where("owner = ? AND id = ? AND expires_at > ? AND deadline_at > ?", owner, id, now, now).
		Updates(map[string]any{"expires_at": min(now+idleTTL, row.DeadlineAt), "active_until": gorm.Expr("CASE WHEN active_until < ? THEN ? ELSE active_until END", now+turnTTL, now+turnTTL), "revision": gorm.Expr("revision + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// MySQL reports zero changed rows for an identical timestamp.
		_, err = LoadConversationSession(ctx, owner, id)
	}
	return err
}

type ConversationCommitInput struct {
	Session                        ConversationSession
	Checkpoint                     ConversationCheckpoint
	Receipt                        ConversationTurnCommit
	Resources                      []ConversationResource
	MaxOwnerBytes, MaxSessionBytes int64
}

func PrepareConversationCommit(ctx context.Context, input ConversationCommitInput) error {
	if DB == nil {
		return errors.New("conversation database unavailable")
	}
	if input.Receipt.RequestID == "" || len(input.Receipt.RequestID) > 64 || input.Checkpoint.ID == "" ||
		input.Checkpoint.Owner == "" || input.Checkpoint.Owner != input.Receipt.Owner || input.Session.Owner != input.Receipt.Owner ||
		input.Checkpoint.SessionID != input.Session.ID || input.Receipt.SessionID != input.Session.ID ||
		input.Receipt.CheckpointID != input.Checkpoint.ID || input.Checkpoint.State != "prepared" || input.Receipt.State != "prepared" ||
		input.Checkpoint.Bytes <= 0 || input.Receipt.ActualQuota < 0 {
		return errors.New("invalid conversation commit")
	}
	return stateDB(ctx).Transaction(func(tx *gorm.DB) error {
		var prior ConversationTurnCommit
		err := tx.First(&prior, "request_id = ?", input.Receipt.RequestID).Error
		if err == nil {
			if prior.Owner != input.Receipt.Owner || prior.CheckpointID != input.Checkpoint.ID || prior.ActualQuota != input.Receipt.ActualQuota || prior.BillingLedgerID != input.Receipt.BillingLedgerID {
				return errors.New("conversation request conflict")
			}
			var checkpoint ConversationCheckpoint
			if err := tx.First(&checkpoint, "id = ? AND owner = ?", prior.CheckpointID, prior.Owner).Error; err != nil {
				return err
			}
			if checkpoint.ContentHash != input.Checkpoint.ContentHash || checkpoint.ParentID != input.Checkpoint.ParentID {
				return errors.New("conversation content conflict")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if input.Checkpoint.Bytes < 0 || input.Checkpoint.Bytes > input.MaxSessionBytes {
			return ErrConversationLimit
		}
		if input.Checkpoint.ParentID == "" {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&input.Session).Error; err != nil {
				return err
			}
		} else {
			var parent ConversationCheckpoint
			if err := tx.Where("id = ? AND owner = ? AND session_id = ? AND state = ?", input.Checkpoint.ParentID, input.Checkpoint.Owner, input.Session.ID, "published").First(&parent).Error; err != nil {
				return ErrConversationMissing
			}
			if input.Checkpoint.Depth != parent.Depth+1 {
				return errors.New("invalid conversation depth")
			}
		}
		ownerUsage := ConversationOwnerUsage{Owner: input.Receipt.Owner}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ownerUsage).Error; err != nil {
			return err
		}
		bytes := input.Checkpoint.Bytes
		result := tx.Model(&ConversationOwnerUsage{}).Where("owner = ? AND bytes <= ?", ownerUsage.Owner, input.MaxOwnerBytes-bytes).
			UpdateColumn("bytes", gorm.Expr("bytes + ?", bytes))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConversationLimit
		}
		now := time.Now().Unix()
		result = tx.Model(&ConversationSession{}).Where("id = ? AND owner = ? AND bytes <= ? AND ((expires_at > ? AND deadline_at > ?) OR active_until > ?)",
			input.Checkpoint.SessionID, ownerUsage.Owner, input.MaxSessionBytes-bytes, now, now, now).
			Updates(map[string]any{"bytes": gorm.Expr("bytes + ?", bytes), "revision": gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConversationLimit
		}
		if err := tx.Create(&input.Checkpoint).Error; err != nil {
			return err
		}
		if err := tx.Create(&input.Receipt).Error; err != nil {
			return err
		}
		if input.Receipt.BillingLedgerID != 0 {
			ledger, err := lockBillingLedgerTx(tx, input.Receipt.BillingLedgerID)
			if err != nil {
				return err
			}
			if ledger.RequestID != input.Receipt.RequestID || (ledger.State != BillingLedgerStateReserved && ledger.State != BillingLedgerStateSettled) {
				return errors.New("conversation billing intent conflict")
			}
			if ledger.State == BillingLedgerStateSettled {
				if ledger.ActualQuota != input.Receipt.ActualQuota || ledger.ChannelID != input.Receipt.ChannelID {
					return errors.New("conversation billing receipt conflict")
				}
			} else {
				if err := assignBillingSettlementChannel(ledger, []int{input.Receipt.ChannelID}); err != nil {
					return err
				}
				ledger.State, ledger.DesiredState, ledger.DesiredQuota = BillingLedgerStateReconcileRequired, BillingLedgerDesiredSettle, input.Receipt.ActualQuota
				ledger.NextRetryAt, ledger.UpdatedAt = now+30, now
				ledger.Version++
				if err := tx.Save(ledger).Error; err != nil {
					return err
				}
			}
		}
		for _, resource := range input.Resources {
			if resource.Owner != input.Receipt.Owner || resource.SessionID != input.Session.ID || resource.CheckpointID != input.Checkpoint.ID || resource.Digest == "" {
				return errors.New("invalid conversation resource ownership")
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&resource).Error; err != nil {
				return err
			}
			var stored ConversationResource
			if err := tx.Where("owner = ? AND kind = ? AND digest = ?", resource.Owner, resource.Kind, resource.Digest).First(&stored).Error; err != nil {
				return err
			}
			if stored.CheckpointID != input.Checkpoint.ID {
				var previous ConversationCheckpoint
				if err := tx.Where("id = ? AND owner = ?", stored.CheckpointID, resource.Owner).First(&previous).Error; err != nil {
					return err
				}
				if previous.ContentHash != input.Checkpoint.ContentHash || previous.ParentID != input.Checkpoint.ParentID {
					return errors.New("ambiguous conversation resource")
				}
			}
		}
		return nil
	})
}

func PublishConversationCommit(ctx context.Context, owner, requestID string) error {
	if DB == nil {
		return errors.New("conversation database unavailable")
	}
	return stateDB(ctx).Transaction(func(tx *gorm.DB) error {
		var receipt ConversationTurnCommit
		if err := tx.Where("owner = ? AND request_id = ?", owner, requestID).First(&receipt).Error; err != nil {
			return err
		}
		if receipt.BillingLedgerID != 0 {
			ledger, err := lockBillingLedgerTx(tx, receipt.BillingLedgerID)
			if err != nil {
				return err
			}
			if ledger.State != BillingLedgerStateSettled || ledger.RequestID != receipt.RequestID || ledger.ActualQuota != receipt.ActualQuota || ledger.ChannelID != receipt.ChannelID {
				return errors.New("conversation billing settlement pending")
			}
		} else if receipt.ActualQuota != 0 {
			return errors.New("conversation billing receipt missing")
		}
		if err := tx.Model(&ConversationCheckpoint{}).Where("owner = ? AND id = ?", owner, receipt.CheckpointID).Update("state", "published").Error; err != nil {
			return err
		}
		return tx.Model(&ConversationTurnCommit{}).Where("owner = ? AND request_id = ? AND state = ?", owner, requestID, "prepared").Update("state", "published").Error
	})
}

func MarkConversationDelivered(ctx context.Context, owner, requestID string) error {
	if DB == nil {
		return errors.New("conversation database unavailable")
	}
	return stateDB(ctx).Model(&ConversationTurnCommit{}).Where("owner = ? AND request_id = ? AND state = ?", owner, requestID, "published").Update("state", "delivered").Error
}

// Cleanup whole expired sessions in bounded transactions, retaining all parent
// nodes while any request is still admitted to a live session.
func CleanupConversationState(ctx context.Context, limit int) error {
	if DB == nil {
		return errors.New("conversation database unavailable")
	}
	var sessions []ConversationSession
	now := time.Now().Unix()
	if err := stateDB(ctx).Where("expires_at <= ? AND active_until <= ?", now, now).Order("expires_at").Limit(max(1, min(limit, 64))).Find(&sessions).Error; err != nil {
		return err
	}
	for _, session := range sessions {
		err := stateDB(ctx).Transaction(func(tx *gorm.DB) error {
			removed := tx.Where("id = ? AND revision = ? AND expires_at <= ? AND active_until <= ?", session.ID, session.Revision, now, now).Delete(&ConversationSession{})
			if removed.Error != nil {
				return removed.Error
			}
			if removed.RowsAffected != 1 {
				return nil
			}
			for _, table := range []any{&ConversationResource{}, &ConversationCheckpoint{}, &ConversationTurnCommit{}} {
				if err := tx.Where("session_id = ?", session.ID).Delete(table).Error; err != nil {
					return err
				}
			}
			return tx.Model(&ConversationOwnerUsage{}).Where("owner = ? AND bytes >= ?", session.Owner, session.Bytes).UpdateColumn("bytes", gorm.Expr("bytes - ?", session.Bytes)).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}
