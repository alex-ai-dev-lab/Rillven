package model

import (
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// State statements can contain encrypted conversations or OAuth receipts.
// Suppress SQL interpolation; service diagnostics report only error categories.
func stateDB(ctx context.Context) *gorm.DB {
	return DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard})
}

// MySQL TEXT is limited to 64 KiB. Durable encrypted state has explicit service
// budgets up to MiB, so use LONGTEXT there and ordinary TEXT on SQLite/Postgres.
type StateBlob string

func (StateBlob) GormDataType() string { return "text" }
func (StateBlob) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db.Dialector.Name() == "mysql" {
		return "longtext"
	}
	return "text"
}
