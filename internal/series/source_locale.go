package series

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// resolveInitialSourceLocale picks the source locale of a new Series: the
// creator's preference, then the request's Accept-Language, then the
// translation runtime default. A failed lookup falls through to the next.
func (s *Service) resolveInitialSourceLocale(ctx context.Context, acceptLanguage string) string {
	if locale := s.memberPreferredSourceLocale(ctx); locale != nil {
		return *locale
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if locale := s.translationRuntimeDefaultLocale(ctx); locale != nil {
		return *locale
	}
	return translation.DefaultLocale
}

func (s *Service) memberPreferredSourceLocale(ctx context.Context) *string {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return nil
	}
	locale, err := s.locales.MemberPreferredLocale(ctx, user.MemberID.String())
	if err != nil {
		slog.ErrorContext(ctx, "Member preferred locale lookup failed; inferring Series source locale from the request", "domain", "series", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
		return nil
	}
	if locale == nil {
		return nil
	}
	return localization.NormalizeSupportedLocale(*locale)
}

func (s *Service) translationRuntimeDefaultLocale(ctx context.Context) *string {
	locale, err := s.locales.DefaultTranslationLocale(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using the default Series source locale", "domain", "series", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
		return nil
	}
	return localization.NormalizeSupportedLocale(locale)
}
