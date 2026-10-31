package memberadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

// PreferredLocales reads the locale a Member prefers for the content they
// author.
type PreferredLocales struct{}

// PreferredLocale returns the stored preferred locale of the live Member
// memberID, or nil when the Member has none or is gone.
func (PreferredLocales) PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error) {
	var preferredLocale *string
	err := db.WithContext(ctx).Model(&model.Member{}).
		Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", memberID).
		Scan(&preferredLocale).Error
	return preferredLocale, err
}
