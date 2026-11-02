package menu

import (
	"context"
	"errors"

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
	var exists bool
	if readErr := s.units.Read(ctx, func(store Store) (readErr error) {
		exists, readErr = store.MenuExists(ctx, menuID)
		return readErr
	}); readErr != nil {
		return readErr
	}
	if !exists {
		return domainerr.ResourceNotFound("menu", menuID)
	}
	return err
}

// requireFreshMenuPermission is the in-transaction Menu authority: an
// inactive principal finds no Menu, and an active one needs action, read
// with the principal fence require applies.
func requireFreshMenuPermission(
	ctx context.Context,
	require mutation.Require,
	menuID string,
	action menuAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return domainerr.AuthenticationRequired()
	}
	if principal.MemberID == "" || principal.IdentityID == "" {
		// The fence finds a principal without a Member or Identity inactive.
		return domainerr.ResourceNotFound("menu", menuID)
	}
	can, err := action(menuID)
	if err != nil {
		return domainerr.ResourceNotFound("menu", menuID).WithCause(err)
	}
	if err := require(can); err != nil {
		if errors.Is(err, identitystate.ErrInactivePrincipal) {
			return domainerr.ResourceNotFound("menu", menuID)
		}
		return err
	}
	return nil
}

func requireMenuGlobalCan(
	ctx context.Context,
	checker menuPermissionChecker,
	can policyv1.Can,
) error {
	principal := auth.GetUser(ctx)
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
	store Store,
	require mutation.Require,
	menuID string,
	action menuAction,
) error {
	if _, err := store.LockMenu(ctx, menuID); err != nil {
		if errors.Is(err, ErrMenuNotFound) {
			return domainerr.ResourceNotFound("menu", menuID)
		}
		return err
	}
	return requireFreshMenuPermission(ctx, require, menuID, action)
}

// RequireViewAndLock is the translation read boundary for one Menu, in the
// translation pipeline's transaction store is bound to: require runs the
// fresh check there.
func RequireViewAndLock(ctx context.Context, store Store, require mutation.Require, menuID string) error {
	return requireMenuPermissionAndLock(ctx, store, require, menuID, policyv1.Menu.View)
}

// RequireEditAndLock is the translation source/target mutation boundary for
// one Menu.
func RequireEditAndLock(ctx context.Context, store Store, require mutation.Require, menuID string) error {
	return requireMenuPermissionAndLock(ctx, store, require, menuID, policyv1.Menu.Edit)
}
