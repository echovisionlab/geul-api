package sitesettings

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// LegalOGBackgroundFileID reads the Site Settings slot that supplies a legal
// route's optional source image; kind is "terms" or "privacy".
func LegalOGBackgroundFileID(ctx context.Context, db *gorm.DB, kind string) (*string, error) {
	var column string
	switch strings.TrimSpace(kind) {
	case "privacy":
		column = "privacy_og_background_file_id"
	case "terms":
		column = "terms_og_background_file_id"
	default:
		return nil, fmt.Errorf("unsupported legal OG entity type %q", kind)
	}
	var result struct {
		FileID *string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).Table("site_settings").
		Select(column + " AS file_id").Where("site_settings.id = 1").Scan(&result).Error; err != nil {
		return nil, err
	}
	return optionalString(result.FileID), nil
}
