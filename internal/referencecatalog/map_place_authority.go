package referencecatalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// mapPlaceAccess authorizes a Map Place mutation inside its transaction: the
// principal's Identity is locked and must be active, and SpiceDB must allow
// can. A denial names the role the action needs, author or administrator,
// instead of the action.
func (s *Service) mapPlaceAccess(can policyv1.Can, adminOnly bool) mutation.Access {
	return mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		denied := domainerr.Denied("author access required")
		if adminOnly {
			denied = domainerr.Denied("admin access required")
		}
		principal := auth.GetUser(ctx)
		if principal == nil {
			return domainerr.New(domainerr.Unauthenticated, "", "authentication required")
		}
		active, err := lockActive(principal)
		if err != nil {
			return fmt.Errorf("lock map place mutation principal: %w", err)
		}
		if !active {
			return denied
		}
		decision, err := auth.AuthorizationDecision(ctx, can)
		if err != nil {
			return domainerr.New(domainerr.Unauthenticated, "", "authentication required").WithCause(err)
		}
		allowed, err := s.spiceDB.Can(ctx, decision)
		if err != nil {
			return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		if !allowed {
			return denied
		}
		return nil
	})
}

// lockMapPlace takes the Map Place row for a mutation.
func lockMapPlace(ctx context.Context, store Store, placeID string) (MapPlace, error) {
	place, err := store.LockMapPlace(ctx, placeID)
	if errors.Is(err, ErrMapPlaceNotFound) {
		return MapPlace{}, domainerr.ResourceNotFound("map place", placeID)
	}
	return place, err
}
