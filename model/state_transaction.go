package model

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Leases are judged by the statement execution clock, not replica wall clocks.
func databaseMillis(tx *gorm.DB) (int64, error) {
	expression := ""
	switch tx.Dialector.Name() {
	case "sqlite":
		expression = "CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)"
	case "postgres":
		expression = "CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000 AS BIGINT)"
	case "mysql":
		expression = "CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000 AS SIGNED)"
	default:
		return 0, errors.New("unsupported coordination database")
	}
	var now int64
	err := tx.Raw("SELECT " + expression).Scan(&now).Error
	return now, err
}

func DatabaseMillis(ctx context.Context) (int64, error) { return databaseMillis(stateDB(ctx)) }

// Retry only rolled-back database work. Never put network IO in this callback.
func retryStateTransaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = db.Transaction(fn)
		if err == nil {
			return nil
		}
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "database is locked") && !strings.Contains(message, "database table is locked") && !strings.Contains(message, "sqlite_busy") && !strings.Contains(message, "deadlock") && !strings.Contains(message, "serialization failure") {
			return err
		}
		if attempt == 3 {
			break
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}
