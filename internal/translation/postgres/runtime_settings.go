package translationpostgres

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

// NewRuntimeSettingsReader reads translation policy in the caller's snapshot.
func NewRuntimeSettingsReader(db *gorm.DB) translation.RuntimeSettingsReader {
	return runtimeSettingsReader{db: db}
}

type runtimeSettingsReader struct{ db *gorm.DB }

func (s runtimeSettingsReader) RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	defaults := translation.DefaultRuntimeSettings()

	var row model.TranslationSettings
	if err := s.db.WithContext(ctx).Select("default_locale, updated_at").First(&row, "id = 1").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return defaults, nil
		}
		return defaults, err
	}
	settings, err := translation.NormalizeRuntimeSettings(translation.RuntimeSettings{
		DefaultLocale: row.DefaultLocale,
		UpdatedAt:     &row.UpdatedAt,
	})
	if err != nil {
		return defaults, err
	}
	return settings, nil
}
