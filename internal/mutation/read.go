package mutation

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// Read runs read in one repeatable-read, read-only transaction, so a use case
// that reads several rows sees one snapshot without opening a transaction
// itself.
func Read(ctx context.Context, db *gorm.DB, read func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

// LockedRead is Read for a read that locks the rows it checks for update,
// such as an authorization root, which a read-only transaction refuses.
func LockedRead(ctx context.Context, db *gorm.DB, read func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}
