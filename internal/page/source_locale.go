package page

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
	user := auth.GetUser(ctx)
	if user != nil && user.Authenticated && db != nil {
		var preferredLocale *string
		if err := db.WithContext(ctx).Model(&model.Member{}).
			Select("preferred_locale").Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).
			Scan(&preferredLocale).Error; err == nil && preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
		return settings.DefaultLocale
	}
	return translation.DefaultLocale
}

func resolveCreateSourceLocale(ctx context.Context, db *gorm.DB, identity auth.IdentityManager, requestedLocale, acceptLanguage string) (string, error) {
	if requestedLocale != "" {
		if locale := localization.NormalizeExactSupportedLocale(requestedLocale); locale != nil {
			return *locale, nil
		}
		return "", errs.InvalidArgument("source_locale", "must be a supported canonical locale")
	}
	return resolveInitialSourceLocale(ctx, db, identity, acceptLanguage), nil
}

func nullableStringEqual(left *string, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
