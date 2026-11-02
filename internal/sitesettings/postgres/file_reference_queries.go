package sitesettingspostgres

import (
	"context"

	"gorm.io/gorm"
)

// SiteImagesReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func SiteImagesReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("site_settings AS ref").Where("(ref.logo_full_light_file_id = ? OR ref.logo_full_dark_file_id = ? OR ref.logo_symbol_light_file_id = ? OR ref.logo_symbol_dark_file_id = ? OR ref.logo_email_light_file_id = ? OR ref.logo_email_dark_file_id = ? OR ref.favicon_file_id = ? OR ref.site_og_background_file_id = ? OR ref.privacy_og_background_file_id = ? OR ref.terms_og_background_file_id = ?)", fileID, fileID, fileID, fileID, fileID, fileID, fileID, fileID, fileID, fileID)
}

// SiteLoadersReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func SiteLoadersReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("site_setting_loader_file AS ref").Where("ref.file_id = ?", fileID)
}
