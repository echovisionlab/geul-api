package release

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// resolveInitialSourceLocale picks a new Release's source locale: the
// creating Member's preference, then Accept-Language, then the translation
// default. A failed lookup falls through to the next choice.
func resolveInitialSourceLocale(ctx context.Context, store Store, acceptLanguage string) string {
	if locale := memberPreferredSourceLocale(ctx, store); locale != nil {
		return *locale
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if locale := translationRuntimeDefaultLocale(ctx, store); locale != nil {
		return *locale
	}
	return translation.DefaultLocale
}

func memberPreferredSourceLocale(ctx context.Context, store Store) *string {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return nil
	}
	preferredLocale, err := store.MemberPreferredLocale(ctx, user.MemberID.String())
	if err != nil || preferredLocale == nil {
		if err != nil {
			slog.ErrorContext(ctx, "Member preferred locale lookup failed; inferring Release source locale", "domain", "release", "operation", "load_member_preferred_locale", failurecause.Attr(err))
		}
		return nil
	}
	return localization.NormalizeSupportedLocale(*preferredLocale)
}

func translationRuntimeDefaultLocale(ctx context.Context, store Store) *string {
	defaultLocale, err := store.TranslationDefaultLocale(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using the built-in default Release source locale", "domain", "release", "operation", "load_translation_default_locale", failurecause.Attr(err))
		return nil
	}
	return localization.NormalizeSupportedLocale(defaultLocale)
}
