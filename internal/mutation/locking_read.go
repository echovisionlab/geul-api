package mutation

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// LockingRead is Read for a read that takes row locks (FOR SHARE, FOR KEY
// SHARE) to wait out a concurrent writer of the rows it projects. PostgreSQL
// refuses a locking SELECT in a read-only transaction, so the snapshot is
// repeatable read without the read-only flag; read must not write.
func LockingRead(ctx context.Context, db *gorm.DB, read func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}
