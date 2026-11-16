package model

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Keep non-content tombstones separately from payload cleanup. Export them
// outside the database snapshot and import them before serving a restored DB.
type ConversationDeletion struct {
	ID        string `gorm:"size:64;primaryKey" json:"id"`
	Owner     string `gorm:"size:64;not null;index" json:"owner"`
	SessionID string `gorm:"size:48;not null;index" json:"session_id"`
	DeletedAt int64  `gorm:"not null" json:"deleted_at"`
}

func conversationDeletionID(owner, session string) string {
	return credentialDigest("conversation-deletion/v1/" + owner + "/" + session)
}

func checkConversationDeleted(tx *gorm.DB, owner, session string) error {
	var count int64
	if err := tx.Model(&ConversationDeletion{}).Where("id = ?", conversationDeletionID(owner, session)).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrConversationMissing
	}
	return nil
}

func CheckConversationDeletion(ctx context.Context, owner, session string) error {
	return checkConversationDeleted(stateDB(ctx), owner, session)
}

func ListConversationDeletions(ctx context.Context, after string, limit int) ([]ConversationDeletion, error) {
	var rows []ConversationDeletion
	err := stateDB(ctx).Where("id > ?", after).Order("id ASC").Limit(max(1, min(limit, 256))).Find(&rows).Error
	return rows, err
}

func ImportConversationDeletions(ctx context.Context, rows []ConversationDeletion) error {
	if len(rows) == 0 || len(rows) > 256 {
		return errors.New("import 1-256 deletion tombstones per batch")
	}
	for i := range rows {
		row := &rows[i]
		if row.Owner == "" || len(row.Owner) > 64 || row.SessionID == "" || len(row.SessionID) > 48 || row.DeletedAt <= 0 || strings.ContainsAny(row.Owner+row.SessionID, "\x00\r\n") {
			return errors.New("invalid deletion tombstone")
		}
		row.ID = conversationDeletionID(row.Owner, row.SessionID)
	}
	return retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	})
}

func ListOwnedConversationSessions(ctx context.Context, owner string, before string, limit int) ([]ConversationSession, error) {
	var rows []ConversationSession
	deleted := stateDB(ctx).Model(&ConversationDeletion{}).Select("session_id").Where("owner = ?", owner)
	query := stateDB(ctx).Where("owner = ? AND id NOT IN (?)", owner, deleted)
	if before != "" {
		query = query.Where("id < ?", before)
	}
	err := query.Order("id DESC").Limit(max(1, min(limit, 100))).Find(&rows).Error
	return rows, err
}

func DeleteOwnedConversationSession(ctx context.Context, owner, id string) error {
	// This transaction commits even when cleanup must wait for an active turn.
	err := retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		var session ConversationSession
		if err := lockForUpdate(tx).Where("id = ? AND owner = ?", id, owner).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationMissing
			}
			return err
		}
		now, err := databaseMillis(tx)
		if err != nil {
			return err
		}
		row := ConversationDeletion{ID: conversationDeletionID(owner, id), Owner: owner, SessionID: id, DeletedAt: now / 1000}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
	if err != nil {
		return err
	}
	return retryStateTransaction(ctx, stateDB(ctx), func(tx *gorm.DB) error {
		var session ConversationSession
		if err := lockForUpdate(tx).Where("id = ? AND owner = ?", id, owner).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationMissing
			}
			return err
		}
		now, err := databaseMillis(tx)
		if err != nil {
			return err
		}
		if session.ActiveUntil > now/1000 {
			return ErrConversationBusy
		}
		var pending int64
		if err := tx.Model(&ConversationTurnCommit{}).Where("session_id = ? AND owner = ? AND state = ?", id, owner, "prepared").Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return ErrConversationBusy
		}
		for _, table := range []any{&ConversationResource{}, &ConversationCheckpoint{}, &ConversationTurnCommit{}} {
			if err := tx.Where("session_id = ? AND owner = ?", id, owner).Delete(table).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id = ? AND owner = ?", id, owner).Delete(&ConversationSession{}).Error; err != nil {
			return err
		}
		result := tx.Model(&ConversationOwnerUsage{}).Where("owner = ? AND bytes >= ?", owner, session.Bytes).UpdateColumn("bytes", gorm.Expr("bytes - ?", session.Bytes))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("conversation owner accounting conflict")
		}
		return nil
	})
}

func DeleteOwnedConversationSessions(ctx context.Context, owner string, limit int) (int, error) {
	var sessions []ConversationSession
	deleted := stateDB(ctx).Model(&ConversationDeletion{}).Select("session_id").Where("owner = ?", owner)
	if err := stateDB(ctx).Where("owner = ? AND id NOT IN (?)", owner, deleted).Order("id ASC").Limit(max(1, min(limit, 128))).Find(&sessions).Error; err != nil {
		return 0, err
	}
	count := 0
	for _, session := range sessions {
		if err := DeleteOwnedConversationSession(ctx, owner, session.ID); err != nil && !errors.Is(err, ErrConversationBusy) && !errors.Is(err, ErrConversationMissing) {
			return count, err
		}
		count++
	}
	return count, nil
}

func cleanupConversationDeletions(ctx context.Context, limit int) error {
	var sessions []ConversationSession
	deleted := stateDB(ctx).Model(&ConversationDeletion{}).Select("session_id")
	now, err := DatabaseMillis(ctx)
	if err != nil {
		return err
	}
	if err := stateDB(ctx).Where("id IN (?) AND active_until <= ?", deleted, now/1000).Limit(max(1, min(limit, 64))).Find(&sessions).Error; err != nil {
		return err
	}
	for _, session := range sessions {
		if err := checkConversationDeleted(stateDB(ctx), session.Owner, session.ID); !errors.Is(err, ErrConversationMissing) {
			if err != nil {
				return err
			}
			continue
		}
		if err := DeleteOwnedConversationSession(ctx, session.Owner, session.ID); err != nil && !errors.Is(err, ErrConversationBusy) && !errors.Is(err, ErrConversationMissing) {
			return err
		}
	}
	return nil
}
