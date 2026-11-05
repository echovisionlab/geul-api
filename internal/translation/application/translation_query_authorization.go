package application

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
)

func (s *Service) authorizeTranslationRead(
	ctx context.Context,
	entityType string,
	entityID string,
) error {
	if isAdmin, err := checkSpiceDBAdmin(ctx, auth.GetUser(ctx), s.spiceDB); err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	} else if isAdmin {
		return nil
	}
	if entityType != "series" {
		return domainerr.AdminRequired()
	}
	return s.units.Read(ctx, func(store Store) error {
		return store.Domains().RequireTranslationRead(ctx, entityType, entityID)
	})
}
