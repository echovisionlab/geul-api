package pagepostgres

import (
	"context"

	"gorm.io/gorm"
)

// PageImagesReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func PageImagesReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("page AS ref").Where("ref.featured_image_file_id = ?", fileID)
}
