package mutation

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// Read runs read in one repeatable-read, read-only transaction, so a use case
// that reads several rows sees one snapshot without opening a transaction
// itself. Inside InSharedTransaction it joins that read-committed transaction, so
// it sees the writes made so far rather than a snapshot of its own.
func Read(ctx context.Context, db *gorm.DB, read func(tx *gorm.DB) error) error {
	if shared := joinedTransaction(ctx); shared != nil {
		return read(shared.db.WithContext(ctx))
	}
	return db.WithContext(ctx).Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}
