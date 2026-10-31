package public

import (
	"context"

	"gorm.io/gorm"
)

// lockedRead runs read in one transaction that share-locks the event it
// projects, so the document, relations and media it reads stay one state. A
// read-only snapshot (mutation.Read) cannot take that lock.
func lockedRead(ctx context.Context, db *gorm.DB, read func(*gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return read(tx)
	})
}
