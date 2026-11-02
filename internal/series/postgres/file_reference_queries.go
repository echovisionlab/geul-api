package postgres

import (
	"context"

	"gorm.io/gorm"
)

// SeriesImagesReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func SeriesImagesReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("series AS ref").Where("ref.featured_image_file_id = ?", fileID)
}
