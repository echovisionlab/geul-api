package pagepostgres

import (
	"context"

	"gorm.io/gorm"
)

func PageTargetSlug(ctx context.Context, db *gorm.DB, id string) (*string, error) {
	var row struct{ Slug *string }
	err := db.WithContext(ctx).Table("page").Select("slug").Where("id = ?", id).Take(&row).Error
	return row.Slug, err
}
