package legal

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
	user := auth.GetUser(ctx)
	if user != nil && user.Authenticated && db != nil {
		var preferredLocale *string
		if err := db.WithContext(ctx).Model(&model.Member{}).
			Select("preferred_locale").
			Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).
			Scan(&preferredLocale).Error; err != nil {
			slog.ErrorContext(ctx, "Failed to load preferred source locale", "failure_cause", failurecause.Message(err))
		} else if preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if db != nil {
		if settings, err := translation.LoadRuntimeSettings(ctx, db); err != nil {
			slog.ErrorContext(ctx, "Failed to load source locale settings", "failure_cause", failurecause.Message(err))
		} else {
			return settings.DefaultLocale
		}
	}
	return translation.DefaultLocale
}
