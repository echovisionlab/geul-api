package form

import (
	"context"
	"errors"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ResolveInitialSourceLocale(ctx context.Context, db *gorm.DB, _ auth.IdentityManager, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated && db != nil {
		var preferred *string
		if err := db.WithContext(ctx).Model(&model.Member{}).Select("preferred_locale").
			Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).Scan(&preferred).Error; err == nil && preferred != nil {
			if locale := localization.NormalizeSupportedLocale(*preferred); locale != nil {
				return *locale
			}
		} else if err != nil {
			slog.ErrorContext(ctx, "Member preferred locale load failed; inferring the source locale from the request", "domain", "form", "event", "form.operation_failed", "operation", "load_member_locale", "outcome", "failed", failurecause.Attr(err))
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	return TranslationDefaultLocale(ctx, db)
}

func NormalizeInitialSourceLocale(ctx context.Context, db *gorm.DB, locale string) string {
	if normalized := localization.NormalizeSupportedLocale(locale); normalized != nil {
		return *normalized
	}
	return TranslationDefaultLocale(ctx, db)
}

func TranslationDefaultLocale(ctx context.Context, db *gorm.DB) string {
	if db != nil {
		if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
			return settings.DefaultLocale
		} else {
			slog.ErrorContext(ctx, "Translation settings load failed; using the built-in default locale", "domain", "form", "event", "form.operation_failed", "operation", "load_default_locale", "outcome", "failed", failurecause.Attr(err))
		}
	}
	return translation.DefaultLocale
}

func LockTranslationRoot(ctx context.Context, db *gorm.DB, formID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	result := db.WithContext(ctx).Table("form").Select("id").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id = ?", formID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound("form", formID)
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	return nil
}
