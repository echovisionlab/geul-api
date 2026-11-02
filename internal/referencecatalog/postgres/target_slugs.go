package referencecatalogpostgres

import (
	"context"

	"gorm.io/gorm"
)

func CategoryTargetSlug(ctx context.Context, db *gorm.DB, id string) (*string, error) {
	var row struct{ Slug *string }
	err := db.WithContext(ctx).Table("category").Select("slug").Where("id = ?", id).Take(&row).Error
	return row.Slug, err
}

func TagTargetSlug(ctx context.Context, db *gorm.DB, id string) (*string, error) {
	var row struct{ Slug *string }
	err := db.WithContext(ctx).Table("tag").Select("slug").Where("id = ?", id).Take(&row).Error
	return row.Slug, err
}
