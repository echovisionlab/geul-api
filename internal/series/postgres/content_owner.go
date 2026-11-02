package postgres

import (
	"context"

	"gorm.io/gorm"
)

// SeriesContentOwner supplies the already locked content owner's row query.
func SeriesContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("series AS owner").Where("owner.id = ?", id)
}
