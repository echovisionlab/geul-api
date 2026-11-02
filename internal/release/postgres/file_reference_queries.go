package releasepostgres

import (
	"context"

	"gorm.io/gorm"
)

// ReleaseFilesReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func ReleaseFilesReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("release_file AS ref").Where("ref.file_id = ?", fileID)
}

// TrackOriginalsReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func TrackOriginalsReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("track AS ref").Where("ref.audio_original_file_id = ?", fileID)
}
