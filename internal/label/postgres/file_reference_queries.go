package labelpostgres

import (
	"context"

	"gorm.io/gorm"
)

// LabelLogosReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func LabelLogosReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("label AS ref").Where("(ref.logo_light_file_id = ? OR ref.logo_dark_file_id = ?)", fileID, fileID)
}
