package series

import (
	"context"
	"log/slog"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func resolveInitialSourceLocale(ctx context.Context, db *gorm.DB, _ auth.IdentityManager, acceptLanguage string) string {
	if locale := memberPreferredSourceLocale(ctx, db); locale != nil {
		return *locale
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if locale := translationRuntimeDefaultLocale(ctx, db); locale != nil {
		return *locale
	}
	return translation.DefaultLocale
}

func memberPreferredSourceLocale(ctx context.Context, db *gorm.DB) *string {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || db == nil {
		return nil
	}
	var locale *string
	if err := db.WithContext(ctx).Model(&model.Member{}).Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).Scan(&locale).Error; err != nil {
		slog.ErrorContext(ctx, "Member preferred locale lookup failed; inferring Series source locale from the request", "domain", "series", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
		return nil
	}
	if locale == nil {
		return nil
	}
	return localization.NormalizeSupportedLocale(*locale)
}

func translationRuntimeDefaultLocale(ctx context.Context, db *gorm.DB) *string {
	settings, err := translation.LoadRuntimeSettings(ctx, db)
	if err != nil {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using the default Series source locale", "domain", "series", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
		return nil
	}
	return localization.NormalizeSupportedLocale(settings.DefaultLocale)
}
