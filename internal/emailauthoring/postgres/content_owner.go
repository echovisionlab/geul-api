package emailauthoringpostgres

import (
	"context"

	"gorm.io/gorm"
)

// EmailTemplateContentOwner supplies the already locked content owner's row query.
func EmailTemplateContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("email_template AS owner").Where("owner.id = ?", id)
}

// EmailLayoutContentOwner supplies the already locked content owner's row query.
func EmailLayoutContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("email_layout AS owner").Where("owner.id = ?", id)
}
