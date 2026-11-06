package mutation

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// ReadLocked runs read in one repeatable-read snapshot that may take row
// locks (FOR SHARE, FOR KEY SHARE), which the read-only transaction of Read
// refuses. A projection that must not see its root deleted or rewritten
// while it reads several rows uses it. read must not write.
func ReadLocked(ctx context.Context, db *gorm.DB, read func(tx *gorm.DB) error) error {
	if shared := joinedTransaction(ctx); shared != nil {
		return read(shared.db.WithContext(ctx))
	}
	return db.WithContext(ctx).Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}

// ReadCurrent runs read in one read-committed transaction that locks the
// rows it reads (FOR UPDATE, FOR SHARE, the principal's Identity), so it
// observes their latest committed state after any mutation holding them
// commits, as a mutation's own Lock step does. A check that must agree with
// the next mutation of the same rows, such as whether upload cleanup is still
// needed, and an authorized read that locks its target and the principal
// before it projects, use it. read must not write.
func ReadCurrent(ctx context.Context, db *gorm.DB, read func(tx *gorm.DB) error) error {
	if shared := joinedTransaction(ctx); shared != nil {
		return read(shared.db.WithContext(ctx))
	}
	return db.WithContext(ctx).Transaction(read)
}
