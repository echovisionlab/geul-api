package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

func resolveInitialSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	_ auth.IdentityManager,
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
		return nil
	}
	return localization.NormalizeSupportedLocale(*preferredLocale)
}

func translationRuntimeDefaultLocale(ctx context.Context, db *gorm.DB) *string {
	settings, err := translation.LoadRuntimeSettings(ctx, db)
	if err != nil {
		return nil
	}
	return localization.NormalizeSupportedLocale(settings.DefaultLocale)
}
