package menu

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type menuPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

type menuAction = auth.ResourceAction

func checkMenuPermission(
	ctx context.Context,
	checker menuPermissionChecker,
	menuID string,
	action menuAction,
	principal *auth.UserInfo,
) error {
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return domainerr.AuthenticationRequired()
	}
	can, err := action(menuID)
	if err != nil {
		return domainerr.ResourceNotFound("menu", menuID).WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.NoPermission(can.Action().Name(), "menu")
	}
	return nil
}

func (s *Service) requireExistingMenuPermission(
	ctx context.Context,
	menuID string,
	action menuAction,
) error {
	err := checkMenuPermission(ctx, s.permissions, menuID, action, auth.GetUser(ctx))
	if mutation.IsDenied(err) {
		return domainerr.ResourceNotFound("menu", menuID)
	}
	return err
}

func (s *Service) requireMenuPermissionOrNotFound(
	ctx context.Context,
	menuID string,
	action menuAction,
) error {
	err := checkMenuPermission(ctx, s.permissions, menuID, action, auth.GetUser(ctx))
	if err == nil || !mutation.IsDenied(err) {
		return err
	}
	var count int64
	if dbErr := s.db.WithContext(ctx).Table("menu").Where("id = ?", menuID).Count(&count).Error; dbErr != nil {
		return dbErr
	}
	if count == 0 {
		return domainerr.ResourceNotFound("menu", menuID)
	}
	return err
}

// requireFreshMenuPermission is the in-transaction Menu authority: an
// inactive principal finds no Menu, and an active one needs action, read
// with the principal fence require applies.
func requireFreshMenuPermission(
	ctx context.Context,
	tx *gorm.DB,
	require mutation.Require,
	menuID string,
	action menuAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return domainerr.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return err
	}
	if !active {
		return domainerr.ResourceNotFound("menu", menuID)
	}
	can, err := action(menuID)
	if err != nil {
		return domainerr.ResourceNotFound("menu", menuID).WithCause(err)
	}
	return require(can)
}

func requireMenuGlobalCan(
	ctx context.Context,
	checker menuPermissionChecker,
	can policyv1.Can,
) error {
	principal := auth.GetUser(ctx)
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return domainerr.AuthenticationRequired()
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.NoPermission(can.Action().Permission(), "platform")
	}
	return nil
}

func requireMenuPermissionAndLock(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	menuID string,
	action menuAction,
) error {
	if _, err := lockMenuForUpdate(ctx, tx, menuID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("menu", menuID)
		}
		return err
	}
	return requireFreshMenuPermission(ctx, tx, func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(ctx, tx, spiceDB, can)
	}, menuID, action)
}

// RequireViewAndLockWithDB is the translation read boundary for one Menu.
func RequireViewAndLockWithDB(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, menuID string) error {
	return requireMenuPermissionAndLock(ctx, tx, spiceDB, menuID, policyv1.Menu.View)
}

// RequireEditAndLockWithDB is the translation source/target mutation boundary
// for one Menu.
func RequireEditAndLockWithDB(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, menuID string) error {
	return requireMenuPermissionAndLock(ctx, tx, spiceDB, menuID, policyv1.Menu.Edit)
}
