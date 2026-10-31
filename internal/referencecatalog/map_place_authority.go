package referencecatalog

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// mapPlaceAccess authorizes a Map Place mutation inside its transaction: the
// principal's Identity is locked and must be active, and SpiceDB must allow
// can. A denial names the role the action needs, author or administrator,
// instead of the action.
func (s *Service) mapPlaceAccess(can policyv1.Can, adminOnly bool) mutation.Access {
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		return requireLockedMapPlaceAuthority(ctx, tx, can, adminOnly, s.spiceDB)
	})
}

func requireLockedMapPlaceAuthority(
	ctx context.Context,
	tx *gorm.DB,
	can policyv1.Can,
	adminOnly bool,
	spiceDB *auth.SpiceDBClient,
) error {
	denied := domainerr.Denied("author access required")
	if adminOnly {
		denied = domainerr.Denied("admin access required")
	}
	principal := auth.GetUser(ctx)
	if principal == nil {
		return domainerr.New(domainerr.Unauthenticated, "", "authentication required")
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
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
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return denied
	}
	return nil
}

// lockMapPlace takes the Map Place row for a mutation.
func lockMapPlace(ctx context.Context, tx *gorm.DB, placeID string) (*model.MapPlace, error) {
	var place model.MapPlace
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&place, "id = ?", placeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("map place", placeID)
		}
		return nil, err
	}
	return &place, nil
}
