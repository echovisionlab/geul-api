package referencecatalog

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func requireLockedMapPlaceAuthority(
	ctx context.Context,
	tx *gorm.DB,
	can policyv1.Can,
	adminOnly bool,
	spiceDB *auth.SpiceDBClient,
) (string, error) {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return "", errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return "", errs.Internal(fmt.Errorf("lock map place mutation principal: %w", err))
	}
	if !active {
		if adminOnly {
			return "", errs.AdminRequired()
		}
		return "", errs.AuthorRequired()
	}
	if spiceDB == nil {
		return "", errs.DependencyUnavailable("SpiceDB")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return "", errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return "", errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		if adminOnly {
			return "", errs.AdminRequired()
		}
		return "", errs.AuthorRequired()
	}
	return principal.MemberID.String(), nil
}

func lockMapPlaceForUpdate(ctx context.Context, tx *gorm.DB, placeID string) (model.MapPlace, error) {
	var place model.MapPlace
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&place, "id = ?", placeID).Error
	return place, err
}
