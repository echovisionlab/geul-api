package postgres

import (
	"context"

	"gorm.io/gorm"
)

func SeriesTargetSlug(ctx context.Context, db *gorm.DB, id string) (*string, error) {
	var row struct{ Slug *string }
	err := db.WithContext(ctx).Table("series").Select("slug").Where("id = ?", id).Take(&row).Error
	return row.Slug, err
}
