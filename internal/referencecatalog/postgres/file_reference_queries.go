package referencecatalogpostgres

import (
	"context"

	"gorm.io/gorm"
)

// ClientLogosReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func ClientLogosReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("client AS ref").Where("(ref.logo_light_file_id = ? OR ref.logo_dark_file_id = ?)", fileID, fileID)
}

// MapPlaceImagesReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func MapPlaceImagesReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("map_place AS ref").Where("ref.image_file_id = ?", fileID)
}
