package postgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// Locales reads the source locale fallbacks of a new Series outside any
// transaction, as autocommit statements: the translation runtime settings
// row is created on first read.
type Locales struct {
	db *gorm.DB
}

var _ series.Locales = Locales{}

// NewLocales returns the locale fallbacks over db.
func NewLocales(db *gorm.DB) (Locales, error) {
	if db == nil {
		return Locales{}, errors.New("series locales database is required")
	}
	return Locales{db: db}, nil
}

func (l Locales) MemberPreferredLocale(ctx context.Context, memberID string) (*string, error) {
	var locale *string
	err := l.db.WithContext(ctx).Model(&model.Member{}).Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", memberID).Scan(&locale).Error
	return locale, err
}

func (l Locales) DefaultTranslationLocale(ctx context.Context) (string, error) {
	settings, err := translation.LoadRuntimeSettings(ctx, l.db)
	return settings.DefaultLocale, err
}
