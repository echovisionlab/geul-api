package form

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// formPermissionChecker is the final object-authorization seam for Form. It
// accepts one complete generated Can descriptor and must not reconstruct roles
// or consult participant rows.
type formPermissionChecker interface {
	Check(context.Context, policyv1.Can) error
}

type formObjectAction uint8

const (
	formActionView formObjectAction = iota + 1
	formActionEdit
	formActionDelete
	formActionManage
)

func (action formObjectAction) can(formID string) (policyv1.Can, error) {
	switch action {
	case formActionView:
		return policyv1.Form.View(formID)
	case formActionEdit:
		return policyv1.Form.Edit(formID)
	case formActionDelete:
		return policyv1.Form.Delete(formID)
	case formActionManage:
		return policyv1.Form.Manage(formID)
	default:
		return policyv1.Can{}, fmt.Errorf("unsupported Form authorization action %d", action)
	}
}

func newFormPermissionChecker(spiceDB *auth.SpiceDBClient, db *gorm.DB) formPermissionChecker {
	return authz.NewSpiceDBResourceChecker(spiceDB, db, "form")
}

// requireFormAction performs exactly one fully-consistent object permission
// check for the requested Form action.
func (s *FormService) requireFormAction(ctx context.Context, formID string, action formObjectAction) error {
	return requireFormAction(ctx, s.authorization, formID, action)
}

func (s *InternalFormService) requireFormAction(ctx context.Context, formID string, action formObjectAction) error {
	return requireFormAction(ctx, s.authorization, formID, action)
}

func requireFormAction(
	ctx context.Context,
	checker formPermissionChecker,
	formID string,
	action formObjectAction,
) error {
	if checker == nil {
		return errs.Internal(fmt.Errorf("form authorization is not configured"))
	}
	can, err := action.can(formID)
	if err != nil {
		return errs.Internal(err)
	}
	return checker.Check(ctx, can)
}

// requireFreshFormAction keeps principal lifecycle locking inside a mutation
// transaction, then performs the action's single Form permission check.
func (s *FormService) requireFreshFormAction(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	action formObjectAction,
) error {
	return requireFreshFormAction(ctx, tx, s.authorization, formID, action)
}

func (s *InternalFormService) requireFreshFormAction(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	action formObjectAction,
) error {
	return requireFreshFormAction(ctx, tx, s.authorization, formID, action)
}

func requireFreshFormAction(
	ctx context.Context,
	tx *gorm.DB,
	checker formPermissionChecker,
	formID string,
	action formObjectAction,
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
		resource, resourceErr := policyv1.Form.Resource(formID)
		if resourceErr != nil {
			return errs.Internal(resourceErr)
		}
		return errs.NoPermission("access", resource.Type())
	}
	return requireFormAction(ctx, checker, formID, action)
}

// requireFreshFormCreation authorizes the only Form mutation for which no
// object exists yet. The platform check is performed exactly once after the
// authenticated principal's lifecycle has been locked.
func (s *FormService) requireFreshFormCreation(ctx context.Context, tx *gorm.DB) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if !active {
		return errs.AdminRequired()
	}
	can, err := policyv1.Form.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return authz.RequirePlatformPermission(ctx, s.spiceDB, can)
}
