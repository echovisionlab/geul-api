package post

import (
	"context"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
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

func resolveInitialSourceLocale(ctx context.Context, store Store, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated {
		if preferredLocale, err := store.MemberPreferredLocale(ctx, user.MemberID.String()); err == nil && preferredLocale != nil {
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
	if defaultLocale, err := store.DefaultTranslationLocale(ctx); err == nil {
		if locale := localization.NormalizeSupportedLocale(defaultLocale); locale != nil {
			return *locale
		}
	} else {
		slog.ErrorContext(ctx, "Localization settings lookup failed; using the default Post source locale", "domain", "post", "operation", "load_source_locale_settings", "outcome", "failed", failurecause.Attr(err))
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

func nullableStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneNullableString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
