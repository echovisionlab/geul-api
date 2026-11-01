package form

import (
	"context"
	"log/slog"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// resolveInitialSourceLocale picks a new Form's source locale: the creator's
// preferred locale, then the request's Accept-Language, then the site
// default.
func (s *Service) resolveInitialSourceLocale(ctx context.Context, db *gorm.DB, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated {
		if preferred, err := s.members.PreferredLocale(ctx, db, user.MemberID.String()); err == nil && preferred != nil {
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
	return translationDefaultLocale(ctx, db)
}

func normalizeInitialSourceLocale(ctx context.Context, db *gorm.DB, locale string) string {
	if normalized := localization.NormalizeSupportedLocale(locale); normalized != nil {
		return *normalized
	}
	return translationDefaultLocale(ctx, db)
}

func translationDefaultLocale(ctx context.Context, db *gorm.DB) string {
	settings, err := translation.LoadRuntimeSettings(ctx, db)
	if err != nil {
		slog.ErrorContext(ctx, "Translation settings load failed; using the built-in default locale", "domain", "form", "event", "form.operation_failed", "operation", "load_default_locale", "outcome", "failed", failurecause.Attr(err))
		return translation.DefaultLocale
	}
	return settings.DefaultLocale
}
