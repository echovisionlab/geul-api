package work

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type workAuthorizationUse uint8
type workAction = auth.ResourceAction

const (
	workAuthorizationRead workAuthorizationUse = iota
	workAuthorizationMutation
)

// RequireLockedView locks the Work root and the principal in another
// domain's transaction, which joined is bound to, and requires View (or
// ViewArchived) for relation policy consumers. A caller without it finds no
// Work.
func RequireLockedView(ctx context.Context, joined Joined, fence identitystate.Fence, checker authz.AuthorizationDecisionChecker, workID string) error {
	_, err := requireLockedWorkPermission(ctx, joined.Store, freshRequire(ctx, joined.Store, fence, checker), workID, policyv1.Work.View, workAuthorizationRead)
	return err
}

// RequireLockedEdit is RequireLockedView for Edit (or EditArchived).
func RequireLockedEdit(ctx context.Context, joined Joined, fence identitystate.Fence, checker authz.AuthorizationDecisionChecker, workID string) error {
	_, err := requireLockedWorkPermission(ctx, joined.Store, freshRequire(ctx, joined.Store, fence, checker), workID, policyv1.Work.Edit, workAuthorizationMutation)
	return err
}

func requireWorkGlobalAction(ctx context.Context, checker authz.AuthorizationDecisionChecker, can policyv1.Can) error {
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
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
		return domainerr.AdminRequired()
	}
	return nil
}

func requireWorkCreate(ctx context.Context, checker authz.AuthorizationDecisionChecker) error {
	can, err := policyv1.Work.Create()
	if err != nil {
		return err
	}
	return requireWorkGlobalAction(ctx, checker, can)
}

func requireWorkList(ctx context.Context, checker authz.AuthorizationDecisionChecker) error {
	can, err := policyv1.Work.List()
	if err != nil {
		return err
	}
	return requireWorkGlobalAction(ctx, checker, can)
}

func workLifecycleAction(status Status, normal workAction, use workAuthorizationUse) workAction {
	if status != StatusArchived {
		return normal
	}
	if use == workAuthorizationRead {
		return policyv1.Work.ViewArchived
	}
	return policyv1.Work.EditArchived
}

func requireWorkPermissionForCurrentActor(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	workID string,
	action workAction,
) error {
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	can, err := action(workID)
	if err != nil {
		return domainerr.ResourceNotFound("work", workID).WithCause(err)
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
		return domainerr.ResourceNotFound("work", workID)
	}
	return nil
}

func requireWorkPermission(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	work Work,
	normal workAction,
	use workAuthorizationUse,
) error {
	return requireWorkPermissionForCurrentActor(ctx, checker, work.ID, workLifecycleAction(work.Status, normal, use))
}

func (s *Service) requireWorkViewOrNotFound(ctx context.Context, work Work) error {
	return requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.View, workAuthorizationRead)
}

func lockWorkAuthorizationRoot(ctx context.Context, store Store, workID string) (Root, error) {
	root, err := store.LockRoot(ctx, workID)
	if errors.Is(err, ErrWorkNotFound) {
		return Root{}, domainerr.ResourceNotFound("work", workID)
	}
	return root, err
}

// freshRequire checks an action for the request principal, its Identity
// locked through fence in store's transaction (identitystate.RequireFreshCan),
// for a check outside a mutation's Access step.
func freshRequire(ctx context.Context, store Store, fence identitystate.Fence, checker authz.AuthorizationDecisionChecker) mutation.Require {
	return func(can policyv1.Can) error {
		return store.RequireFreshCan(ctx, fence, checker, can)
	}
}

// requireLockedWorkPermission locks the Work root and the principal, then
// requires the one action the locked lifecycle selects from normal. A
// caller without it, inactive or denied, finds no Work.
func requireLockedWorkPermission(
	ctx context.Context,
	store Store,
	require mutation.Require,
	workID string,
	normal workAction,
	use workAuthorizationUse,
) (Root, error) {
	root, err := lockWorkAuthorizationRoot(ctx, store, workID)
	if err != nil {
		return Root{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return Root{}, domainerr.AuthenticationRequired()
	}
	can, err := workLifecycleAction(root.Status, normal, use)(workID)
	if err != nil {
		return Root{}, domainerr.ResourceNotFound("work", workID).WithCause(err)
	}
	if err := require(can); err != nil {
		if errors.Is(err, identitystate.ErrInactivePrincipal) {
			return Root{}, domainerr.ResourceNotFound("work", workID)
		}
		if mutation.IsDenied(err) {
			return Root{}, domainerr.ResourceNotFound("work", workID).WithCause(err)
		}
		return Root{}, err
	}
	return root, nil
}

// lockedWork is the access of a Work mutation: the root is locked before
// the one action its lifecycle requires is checked. locked, when set,
// receives the locked root.
func lockedWork(workID string, normal workAction, locked *Root) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		root, err := requireLockedWorkPermission(ctx, store, require, workID, normal, workAuthorizationMutation)
		if locked != nil {
			*locked = root
		}
		return err
	})
}
