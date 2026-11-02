package artistpostgres

import (
	"context"

	"gorm.io/gorm"
)

// ArtistFilesReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func ArtistFilesReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("artist_file AS ref").Where("ref.file_id = ?", fileID)
}
