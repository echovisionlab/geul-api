package release

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

func resolveInitialSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	acceptLanguage string,
) string {
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
	var preferredLocale *string
	if err := db.WithContext(ctx).Model(&model.Member{}).
		Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).
		Scan(&preferredLocale).Error; err != nil || preferredLocale == nil {
		if err != nil {
			slog.ErrorContext(ctx, "Member preferred locale lookup failed; inferring Release source locale", "domain", "release", "operation", "load_member_preferred_locale", failurecause.Attr(err))
		}
		return nil
	}
	return localization.NormalizeSupportedLocale(*preferredLocale)
}

func translationRuntimeDefaultLocale(ctx context.Context, db *gorm.DB) *string {
	settings, err := translation.LoadRuntimeSettings(ctx, db)
	if err != nil {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using the built-in default Release source locale", "domain", "release", "operation", "load_translation_default_locale", failurecause.Attr(err))
		return nil
	}
	return localization.NormalizeSupportedLocale(settings.DefaultLocale)
}
