package form

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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

// requireFormAction performs exactly one fully-consistent object permission
// check for the requested Form action.
func (s *Service) requireFormAction(ctx context.Context, formID string, action formObjectAction) error {
	return requireFormAction(ctx, s.authorization, formID, action)
}

func requireFormAction(ctx context.Context, checker formPermissionChecker, formID string, action formObjectAction) error {
	can, err := action.can(formID)
	if err != nil {
		return err
	}
	return checker.Check(ctx, can)
}

// requireVisibleFormAction is requireFormAction for a read: a caller without
// the action finds no Form, answered by notFound.
func (s *Service) requireVisibleFormAction(ctx context.Context, formID string, action formObjectAction, notFound error) error {
	if err := s.requireFormAction(ctx, formID, action); err != nil {
		if mutation.IsDenied(err) {
			return notFound
		}
		return err
	}
	return nil
}

// requireFreshFormAction locks the principal's Identity through the fence,
// then performs the action's single Form permission check. An inactive
// principal answers "no permission to access this form", Form's own
// wording, which is why Form checks through mutation.Fenced.
func (s *Service) requireFreshFormAction(ctx context.Context, lockActive func(*auth.UserInfo) (bool, error), formID string, action formObjectAction) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return domainerr.AuthenticationRequired()
	}
	active, err := lockActive(principal)
	if err != nil {
		return err
	}
	if !active {
		resource, err := policyv1.Form.Resource(formID)
		if err != nil {
			return err
		}
		return domainerr.NoPermission("access", resource.Type())
	}
	return s.requireFormAction(ctx, formID, action)
}

// freshFormAction is the access of a Form mutation whose root Lock already
// holds.
func (s *Service) freshFormAction(formID string, action formObjectAction) mutation.Access {
	return mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		return s.requireFreshFormAction(ctx, lockActive, formID, action)
	})
}

// freshFormCreation authorizes the only Form mutation for which no object
// exists yet. The platform check is performed exactly once after the
// authenticated principal's lifecycle has been locked.
func (s *Service) freshFormCreation() mutation.Access {
	return mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		principal := auth.GetUser(ctx)
		if principal == nil {
			return domainerr.AuthenticationRequired()
		}
		active, err := lockActive(principal)
		if err != nil {
			return err
		}
		if !active {
			return domainerr.AdminRequired()
		}
		can, err := policyv1.Form.Create()
		if err != nil {
			return err
		}
		return authz.RequirePlatformPermission(ctx, s.spiceDB, can)
	})
}

// requirePlatformFormAction checks a platform-scoped Form action outside a
// transaction.
func (s *Service) requirePlatformFormAction(ctx context.Context, action func() (policyv1.Can, error)) error {
	can, err := action()
	if err != nil {
		return err
	}
	return authz.RequirePlatformPermission(ctx, s.spiceDB, can)
}

// lockFormRoot locks the Form row of a management mutation before its
// authorization and returns its stored ID; a missing Form answers notFound.
func lockFormRoot(ctx context.Context, store Store, formID string, notFound error) (string, error) {
	id, err := store.LockFormRoot(ctx, formID)
	return id, formNotFound(err, notFound)
}

// formNotFound answers notFound for a missing Form.
func formNotFound(err, notFound error) error {
	if errors.Is(err, ErrFormNotFound) {
		return notFound
	}
	return err
}
