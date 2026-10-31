package programevent

import (
	"context"

	"gorm.io/gorm"
)

// lockedRead runs read in one transaction that takes row and Identity
// locks: an authorized read locks the event and the principal's lifecycle so
// that the authorization and the projection see one state. A read-only
// snapshot (mutation.Read) cannot take those locks, so this is the one
// transaction the Program Event reads open themselves.
func lockedRead(ctx context.Context, db *gorm.DB, read func(*gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return read(tx)
	})
}
