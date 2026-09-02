package menu

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
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
		return errs.DependencyUnavailable("SpiceDB")
	}
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return errs.AuthenticationRequired()
	}
	can, err := action(menuID)
	if err != nil {
		return errs.NotFound("menu", menuID)
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), "menu")
	}
	return nil
}

func (s *MenuService) requireExistingMenuPermission(
	ctx context.Context,
	menuID string,
	action menuAction,
) error {
	err := checkMenuPermission(ctx, s.permissions, menuID, action, auth.GetUser(ctx))
	if connect.CodeOf(err) == connect.CodePermissionDenied {
		return errs.NotFound("menu", menuID)
	}
	return err
}

func (s *MenuService) requireMenuPermissionOrNotFound(
	ctx context.Context,
	menuID string,
	action menuAction,
) error {
	err := checkMenuPermission(ctx, s.permissions, menuID, action, auth.GetUser(ctx))
	if err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		return err
	}
	var count int64
	if dbErr := s.db.WithContext(ctx).Table("menu").Where("id = ?", menuID).Count(&count).Error; dbErr != nil {
		return errs.Internal(dbErr)
	}
	if count == 0 {
		return errs.NotFound("menu", menuID)
	}
	return err
}

func requireFreshMenuPermission(
	ctx context.Context,
	tx *gorm.DB,
	checker menuPermissionChecker,
	menuID string,
	action menuAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if !active {
		return errs.NotFound("menu", menuID)
	}
	return checkMenuPermission(ctx, checker, menuID, action, principal)
}

func requireMenuGlobalCan(
	ctx context.Context,
	checker menuPermissionChecker,
	can policyv1.Can,
) error {
	principal := auth.GetUser(ctx)
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return errs.AuthenticationRequired()
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Permission(), "platform")
	}
	return nil
}

func requireFreshMenuGlobalCan(
	ctx context.Context,
	tx *gorm.DB,
	checker menuPermissionChecker,
	can policyv1.Can,
) error {
	return identitystate.RequireFreshAdminCan(ctx, tx, checker, can)
}

func requireMenuPermissionAndLock(
	ctx context.Context,
	tx *gorm.DB,
	checker menuPermissionChecker,
	menuID string,
	action menuAction,
) error {
	if _, err := lockMenuForUpdate(ctx, tx, menuID); err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound("menu", menuID)
		}
		return errs.Internal(err)
	}
	return requireFreshMenuPermission(ctx, tx, checker, menuID, action)
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
