package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mutation"
)

func (s *Service) requireOwnerPreflight(ctx context.Context, kind, id string, edit bool) error {
	if s.ownerPreflights != nil {
		return s.ownerPreflights.Read(ctx, func(store Store) error { return store.RequirePolicyOwner(ctx, kind, id, edit) })
	}
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error { return unit.Store().RequirePolicyOwner(ctx, kind, id, edit) })
}
