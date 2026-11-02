package translationpostgres

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/lib/pq"
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

func (s *store) MachineTranslationTargets(ctx context.Context) ([]application.Locale, error) {
	locales, err := localization.NewCatalog(s.tx).MachineTranslationTargets(ctx)
	if err != nil {
		return nil, err
	}
	return localesOf(locales), nil
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
		Code:                      row.Code,
		DisplayName:               row.DisplayName,
		Enabled:                   row.Enabled,
		IsPublic:                  row.IsPublic,
		Dir:                       row.Dir,
		MachineTranslationAllowed: row.MachineTranslationAllowed,
		FontProfile:               row.FontProfile,
		SortOrder:                 row.SortOrder,
	}
}

func (s *store) RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	defaults := translation.DefaultRuntimeSettings()
	if err := s.tx.WithContext(ctx).Exec(
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

	return (runtimeSettingsReader{db: s.tx}).RuntimeSettings(ctx)
}

func (s *store) LockRuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	var row model.TranslationSettings
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = 1").Error; err != nil {
		return translation.RuntimeSettings{}, err
	}
	updatedAt := row.UpdatedAt
	return translation.RuntimeSettings{
		DefaultLocale:  row.DefaultLocale,
		ProtectedTerms: append([]string(nil), row.ProtectedTerms...),
		UpdatedAt:      &updatedAt,
	}, nil
}

func (s *store) UpdateRuntimeSettings(ctx context.Context, change application.SettingsChange) (time.Time, error) {
	row := model.TranslationSettings{ID: 1}
	updates := map[string]any{"updated_at": s.tx.NowFunc().UTC()}
	if change.DefaultLocale != nil {
		updates["default_locale"] = *change.DefaultLocale
	}
	if change.TermsChanged {
		updates["protected_terms"] = pq.Array(change.ProtectedTerms)
	}
	if err := s.tx.WithContext(ctx).Model(&row).Clauses(clause.Returning{Columns: []clause.Column{{Name: "updated_at"}}}).Updates(updates).Error; err != nil {
		return time.Time{}, err
	}
	return row.UpdatedAt, nil
}

func (s *store) SiteNames(ctx context.Context) (string, string, error) {
	var siteSettings model.SiteSettings
	if err := s.tx.WithContext(ctx).Select("site_title", "company_name").Where("id = 1").Take(&siteSettings).Error; err != nil {
		return "", "", err
	}
	return siteSettings.SiteTitle, siteSettings.CompanyName, nil
}

func (s *store) ActiveProviders(ctx context.Context) ([]application.Provider, error) {
	var providers []model.TranslationProviderConfig
	if err := s.tx.WithContext(ctx).
		Where("is_active = ?", true).
		Order("priority ASC, created_at ASC").
		Find(&providers).Error; err != nil {
		return nil, err
	}
	return providersOf(providers), nil
}

func (s *store) ListProviders(ctx context.Context, query application.ProviderListQuery) ([]application.Provider, int64, error) {
	db := s.tx.WithContext(ctx).Model(&model.TranslationProviderConfig{})
	if query.ActiveOnly {
		db = db.Where("is_active = ?", true)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var providers []model.TranslationProviderConfig
	if err := db.Order("priority ASC, created_at ASC").Limit(int(query.Limit)).Offset(int(query.Offset)).Find(&providers).Error; err != nil {
		return nil, 0, err
	}
	return providersOf(providers), total, nil
}

func (s *store) LockProvider(ctx context.Context, providerID string) (application.Provider, error) {
	var provider model.TranslationProviderConfig
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&provider, "id = ?", providerID).Error; err != nil {
		return application.Provider{}, notFound(err, application.ErrProviderNotFound)
	}
	return providerOf(provider), nil
}

func (s *store) InsertProvider(ctx context.Context, provider application.Provider) (application.Provider, error) {
	row := providerRow(provider)
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return application.Provider{}, err
	}
	return providerOf(row), nil
}

func (s *store) UpdateProvider(ctx context.Context, providerID string, change application.ProviderChange) (time.Time, error) {
	row := model.TranslationProviderConfig{ID: providerID}
	updates := structured.Fields{"updated_at": s.tx.NowFunc().UTC()}
	if change.Name != nil {
		updates["name"] = *change.Name
	}
	if change.IsActive != nil {
		updates["is_active"] = *change.IsActive
	}
	if change.Priority != nil {
		updates["priority"] = *change.Priority
	}
	if change.Type != nil {
		updates["type"] = model.TranslationProviderType(*change.Type)
	}
	if change.Config != nil {
		updates["config"] = model.TranslationProviderConfigJSON{RawMessage: json.RawMessage(change.Config)}
	}
	if err := s.tx.WithContext(ctx).Model(&row).Clauses(clause.Returning{Columns: []clause.Column{{Name: "updated_at"}}}).Updates(updates).Error; err != nil {
		return time.Time{}, err
	}
	return row.UpdatedAt, nil
}

func (s *store) DeleteProvider(ctx context.Context, providerID string) error {
	return s.tx.WithContext(ctx).Delete(&model.TranslationProviderConfig{ID: providerID}).Error
}

func providersOf(rows []model.TranslationProviderConfig) []application.Provider {
	providers := make([]application.Provider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, providerOf(row))
	}
	return providers
}

func providerOf(row model.TranslationProviderConfig) application.Provider {
	return application.Provider{
		ID:        row.ID,
		Name:      row.Name,
		Type:      application.ProviderType(row.Type),
		IsActive:  row.IsActive,
		Priority:  row.Priority,
		Config:    row.Config.RawMessage,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func providerRow(provider application.Provider) model.TranslationProviderConfig {
	return model.TranslationProviderConfig{
		ID:        provider.ID,
		Name:      provider.Name,
		Type:      model.TranslationProviderType(provider.Type),
		IsActive:  provider.IsActive,
		Priority:  provider.Priority,
		Config:    model.TranslationProviderConfigJSON{RawMessage: json.RawMessage(provider.Config)},
		CreatedAt: provider.CreatedAt,
		UpdatedAt: provider.UpdatedAt,
	}
}
