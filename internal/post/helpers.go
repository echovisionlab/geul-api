package post

import (
	"context"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func normalizeOptionalNullableString(raw *string) (*string, bool) {
	if raw == nil {
		return nil, false
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, true
	}
	return &trimmed, true
}

func validateSlugWithoutSlash(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}

func resolveInitialSourceLocale(ctx context.Context, db *gorm.DB, _ auth.IdentityManager, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated && db != nil {
		var preferredLocale *string
		if err := db.WithContext(ctx).Model(&model.Member{}).
			Select("preferred_locale").
			Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).
			Scan(&preferredLocale).Error; err == nil && preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		} else if err != nil {
			slog.ErrorContext(ctx, "Preferred locale lookup failed; inferring the Post source locale", "domain", "post", "operation", "load_preferred_locale", "outcome", "failed", failurecause.Attr(err))
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
		if locale := localization.NormalizeSupportedLocale(settings.DefaultLocale); locale != nil {
			return *locale
		}
	} else {
		slog.ErrorContext(ctx, "Localization settings lookup failed; using the default Post source locale", "domain", "post", "operation", "load_source_locale_settings", "outcome", "failed", failurecause.Attr(err))
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
