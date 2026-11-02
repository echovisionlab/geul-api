package menupostgres

import (
	"context"

	"gorm.io/gorm"
)

// MenuContentOwner supplies the already locked content owner's row query.
func MenuContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("menu AS owner").Where("owner.id = ?", id)
}
