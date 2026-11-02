package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

func (s *Service) resolveRequestedLocales(
	ctx context.Context,
	store Store,
	sourceLocale string,
	requested []string,
) ([]string, error) {
	locales, err := store.MachineTranslationTargets(ctx)
	if err != nil {
		return nil, err
	}

	if len(requested) == 0 {
		resolved := make([]string, 0, len(locales))
		for _, locale := range locales {
			if locale.Code == sourceLocale {
				continue
			}
			resolved = append(resolved, locale.Code)
		}
		return resolved, nil
	}

	allowed := make(map[string]struct{}, len(locales))
	for _, locale := range locales {
		allowed[locale.Code] = struct{}{}
	}
	resolved := make([]string, 0, len(requested))
	seen := make(map[string]struct{})
	for _, code := range requested {
		code = strings.TrimSpace(code)
		if code == "" || code == sourceLocale {
			continue
		}
		if _, ok := allowed[code]; !ok {
			return nil, domainerr.InvalidField("locales", fmt.Sprintf("unsupported locale %q", code))
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		resolved = append(resolved, code)
	}
	return resolved, nil
}

// requireKnownLocale answers an unknown locale code as an invalid field.
func requireKnownLocale(ctx context.Context, store Store, field, code string) error {
	if _, err := store.Locale(ctx, code); err != nil {
		if errors.Is(err, ErrLocaleNotFound) {
			return domainerr.InvalidField(field, "unsupported locale")
		}
		return err
	}
	return nil
}
