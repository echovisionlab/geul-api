package post

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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

func normalizeStringIDs(ids []string) []string {
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
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
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
		if locale := localization.NormalizeSupportedLocale(settings.DefaultLocale); locale != nil {
			return *locale
		}
	}
	return translation.DefaultLocale
}
