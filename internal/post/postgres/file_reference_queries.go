package postpostgres

import (
	"context"

	"gorm.io/gorm"
)

// PostImagesReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func PostImagesReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("post AS ref").Where("ref.featured_image_file_id = ?", fileID)
}
