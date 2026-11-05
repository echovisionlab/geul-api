package application

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

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
