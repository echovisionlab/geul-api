package legalpostgres

import (
	"context"

	"gorm.io/gorm"
)

// PrivacyContentOwner supplies the already locked content owner's row query.
func PrivacyContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("privacy_history AS owner").Where("owner.id = ?", id)
}

// TermsContentOwner supplies the already locked content owner's row query.
func TermsContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("terms_history AS owner").Where("owner.id = ?", id)
}
