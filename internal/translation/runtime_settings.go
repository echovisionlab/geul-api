package translation

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

const DefaultLocale = localization.LocaleEnglish

// RuntimeSettings is the singleton authority for the default locale and exact
// case-sensitive spellings that every translation provider must preserve.
type RuntimeSettings struct {
	DefaultLocale  string
	ProtectedTerms []string
	UpdatedAt      *time.Time
}

func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{
		DefaultLocale: DefaultLocale,
	}
}

func NormalizeRuntimeSettings(input RuntimeSettings) (RuntimeSettings, error) {
	settings := input
	if normalized := localization.NormalizeSupportedLocale(settings.DefaultLocale); normalized != nil {
		settings.DefaultLocale = *normalized
	} else {
		return RuntimeSettings{}, fmt.Errorf("default locale must be a supported locale")
	}
	settings.ProtectedTerms = NormalizeProtectedTerms(settings.ProtectedTerms)
	return settings, nil
}

func LoadRuntimeSettings(ctx context.Context, db *gorm.DB) (RuntimeSettings, error) {
	defaults := DefaultRuntimeSettings()
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO translation_settings (
			id, default_locale, protected_terms
		) VALUES (?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		1,
		defaults.DefaultLocale,
		"{}",
	).Error; err != nil {
		return defaults, err
	}

	var row model.TranslationSettings
	if err := db.WithContext(ctx).First(&row, "id = 1").Error; err != nil {
		return defaults, err
	}
	settings, err := NormalizeRuntimeSettings(RuntimeSettings{
		DefaultLocale:  row.DefaultLocale,
		ProtectedTerms: append([]string(nil), row.ProtectedTerms...),
		UpdatedAt:      &row.UpdatedAt,
	})
	if err != nil {
		return defaults, err
	}
	return settings, nil
}
