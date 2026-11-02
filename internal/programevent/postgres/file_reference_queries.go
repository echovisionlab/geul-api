package programeventpostgres

import (
	"context"

	"gorm.io/gorm"
)

// EventMediaReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func EventMediaReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("program_event_media AS ref").Where("ref.file_id = ?", fileID)
}

// SeriesPostersReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func SeriesPostersReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("program_event_series AS ref").Where("ref.poster_file_id = ?", fileID)
}
