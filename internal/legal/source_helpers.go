package legal

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func resolveInitialSourceLocale(
	ctx context.Context,
	store Store,
	acceptLanguage string,
) string {
	user := auth.GetUser(ctx)
	if user != nil && user.Authenticated {
		if preferredLocale, err := store.PreferredLocale(ctx, user.MemberID.String()); err != nil {
			slog.ErrorContext(ctx, "Member preferred locale load failed; inferring the source locale from the request", "domain", "legal", "operation", "load_member_locale", "outcome", "failed", failurecause.Attr(err))
		} else if preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := store.TranslationSettings(ctx); err != nil {
		slog.ErrorContext(ctx, "Translation settings load failed; using the built-in default locale", "domain", "legal", "operation", "load_default_locale", "outcome", "failed", failurecause.Attr(err))
	} else {
		return settings.DefaultLocale
	}
	return translation.DefaultLocale
}
