package legal

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// resolveInitialSourceLocale chooses the locale after create authorization.
// The Store isolates optional lookups so failures can fall back in this transaction.
func resolveInitialSourceLocale(ctx context.Context, store Store, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated {
		preferredLocale, err := store.PreferredLocale(ctx, user.MemberID.String())
		if err != nil {
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
	settings, err := store.TranslationSettings(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "Translation settings load failed; using the built-in default locale", "domain", "legal", "operation", "load_default_locale", "outcome", "failed", failurecause.Attr(err))
		return translation.DefaultLocale
	}
	return settings.DefaultLocale
}
