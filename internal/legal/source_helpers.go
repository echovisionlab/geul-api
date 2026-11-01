package legal

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// resolveInitialSourceLocale is the source locale of a new version: the
// Member's preferred locale, then acceptLanguage, then the site default.
// Each lookup reads on its own before the create transaction, so one that
// fails leaves the next and the create intact.
func (s *Service) resolveInitialSourceLocale(ctx context.Context, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated {
		var preferredLocale *string
		err := s.units.Read(ctx, func(store Store) error {
			var err error
			preferredLocale, err = store.PreferredLocale(ctx, user.MemberID.String())
			return err
		})
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
	// Reading the settings stores the defaults first, which a read-only
	// snapshot refuses.
	var settings translation.RuntimeSettings
	if err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		settings, err = store.TranslationSettings(ctx)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Translation settings load failed; using the built-in default locale", "domain", "legal", "operation", "load_default_locale", "outcome", "failed", failurecause.Attr(err))
		return translation.DefaultLocale
	}
	return settings.DefaultLocale
}
