package page

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func resolveInitialSourceLocale(ctx context.Context, store Store, acceptLanguage string) string {
	user := auth.GetUser(ctx)
	if user != nil && user.Authenticated {
		if preferredLocale, err := store.PreferredLocale(ctx, user.MemberID.String()); err == nil && preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		} else if err != nil {
			slog.ErrorContext(ctx, "Preferred locale lookup failed; inferring the Page source locale", "domain", "page", "operation", "load_preferred_locale", "outcome", "failed", failurecause.Attr(err))
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := store.RuntimeSettings(ctx); err == nil {
		return settings.DefaultLocale
	} else {
		slog.ErrorContext(ctx, "Localization settings lookup failed; using the default Page source locale", "domain", "page", "operation", "load_source_locale_settings", "outcome", "failed", failurecause.Attr(err))
	}
	return translation.DefaultLocale
}

func resolveCreateSourceLocale(ctx context.Context, store Store, requestedLocale, acceptLanguage string) (string, error) {
	if requestedLocale != "" {
		if locale := localization.NormalizeExactSupportedLocale(requestedLocale); locale != nil {
			return *locale, nil
		}
		return "", domainerr.InvalidField("source_locale", "must be a supported canonical locale")
	}
	return resolveInitialSourceLocale(ctx, store, acceptLanguage), nil
}

func nullableStringEqual(left *string, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
