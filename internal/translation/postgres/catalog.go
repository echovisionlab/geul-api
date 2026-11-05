package translationpostgres

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

func (s *store) Locales(ctx context.Context, enabledOnly bool) ([]application.Locale, error) {
	catalog := localization.NewCatalog(s.tx)
	var locales []localization.RuntimeLocale
	var err error
	if enabledOnly {
		locales, err = catalog.Enabled(ctx)
	} else {
		locales, err = catalog.All(ctx)
	}
	if err != nil {
		return nil, err
	}
	return localesOf(locales), nil
}

func (s *store) Locale(ctx context.Context, code string) (application.Locale, error) {
	locale, err := localization.NewCatalog(s.tx).Find(ctx, code)
	if err != nil {
		return application.Locale{}, notFound(err, application.ErrLocaleNotFound)
	}
	return localeOf(locale), nil
}

func localesOf(rows []localization.RuntimeLocale) []application.Locale {
	locales := make([]application.Locale, 0, len(rows))
	for _, row := range rows {
		locales = append(locales, localeOf(row))
	}
	return locales
}

func localeOf(row localization.RuntimeLocale) application.Locale {
	return application.Locale{
		Code:        row.Code,
		DisplayName: row.DisplayName,
		Enabled:     row.Enabled,
		IsPublic:    row.IsPublic,
		Dir:         row.Dir,
		FontProfile: row.FontProfile,
		SortOrder:   row.SortOrder,
	}
}

func (s *store) RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	defaults := translation.DefaultRuntimeSettings()
	if err := s.tx.WithContext(ctx).Exec(
		`INSERT INTO translation_settings (
			id, default_locale
		) VALUES (?, ?)
		ON CONFLICT (id) DO NOTHING`,
		1,
		defaults.DefaultLocale,
	).Error; err != nil {
		return defaults, err
	}

	return (runtimeSettingsReader{db: s.tx}).RuntimeSettings(ctx)
}

func (s *store) LockRuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	var row model.TranslationSettings
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = 1").Error; err != nil {
		return translation.RuntimeSettings{}, err
	}
	updatedAt := row.UpdatedAt
	return translation.RuntimeSettings{
		DefaultLocale: row.DefaultLocale,
		UpdatedAt:     &updatedAt,
	}, nil
}

func (s *store) UpdateRuntimeSettings(ctx context.Context, change application.SettingsChange) (time.Time, error) {
	row := model.TranslationSettings{ID: 1}
	updates := map[string]any{"updated_at": s.tx.NowFunc().UTC()}
	if change.DefaultLocale != nil {
		updates["default_locale"] = *change.DefaultLocale
	}
	if err := s.tx.WithContext(ctx).Model(&row).Clauses(clause.Returning{Columns: []clause.Column{{Name: "updated_at"}}}).Updates(updates).Error; err != nil {
		return time.Time{}, err
	}
	return row.UpdatedAt, nil
}
