package page

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type pageAction = auth.ResourceAction

// Authorizer is the SpiceDB port of the Page use cases: the fresh checks
// and relationship writes of the mutation pipeline, a collaboration
// contributor's authority and the relationship snapshot a deletion removes.
// *auth.SpiceDBClient implements it.
type Authorizer interface {
	mutation.Authorizer
	CheckActorCan(ctx context.Context, actor policyv1.Actor, can policyv1.Can) (bool, error)
	SnapshotResourceRelationshipDescriptors(ctx context.Context, plan policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error)
}

// PolicyAuthority exposes the Page-owned root and active-principal fence to
// exact relation policy consumers without duplicating Page authorization.
type PolicyAuthority struct {
	checker pageaccess.PermissionChecker
}

func NewPolicyAuthority(checker pageaccess.PermissionChecker) *PolicyAuthority {
	return &PolicyAuthority{checker: checker}
}

// RequireLockedView locks the Page root in the joined transaction and
// requires the principal to view it under its audience.
func (a *PolicyAuthority) RequireLockedView(ctx context.Context, store Store, pageID string) error {
	if a == nil {
		return domainerr.DependencyUnavailable("Page policy access")
	}
	if err := requireLockedPagePermission(ctx, store, a.require(ctx, store), pageID, policyv1.Page.View); err != nil {
		return err
	}
	policy, err := store.AccessPolicy(ctx, pageID)
	if err != nil {
		return err
	}
	reason, err := pageaccess.Evaluate(ctx, store, a.checker, pageID, policy)
	if err != nil {
		return err
	}
	if reason != pageaccess.ReasonAllowed {
		return domainerr.Denied("you do not have permission to access this page")
	}
	return nil
}

// RequireLockedEdit locks the Page root in the joined transaction and
// requires the principal to edit it.
func (a *PolicyAuthority) RequireLockedEdit(ctx context.Context, store Store, pageID string) error {
	if a == nil {
		return domainerr.DependencyUnavailable("Page policy access")
	}
	return requireLockedPagePermission(ctx, store, a.require(ctx, store), pageID, policyv1.Page.Edit)
}

func (a *PolicyAuthority) require(ctx context.Context, store Store) mutation.Require {
	return func(can policyv1.Can) error { return requireFreshCan(ctx, store, a.checker, can) }
}

// requireFreshCan is the fresh check of mutation.Locked through the Store's
// identity fence, for a read that authorizes in its own snapshot or a step
// in another domain's transaction: an active, onboarded principal that
// holds can, read with full consistency.
func requireFreshCan(ctx context.Context, store Store, checker authz.AuthorizationDecisionChecker, can policyv1.Can) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	active, err := store.LockActivePrincipal(ctx, principal)
	if err != nil {
		return fmt.Errorf("lock active principal: %w", err)
	}
	if !active {
		if can.Valid() {
			return domainerr.NoPermission(can.Action().Name(), can.Resource().Type())
		}
		return domainerr.NoPermission("access", "resource")
	}
	return authz.RequireCan(ctx, checker, can)
}

// requirePagePermission checks action on the Page outside a transaction. A
// caller without it finds no Page.
func requirePagePermission(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	pageID string,
	action pageAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	can, err := action(pageID)
	if err != nil {
		return domainerr.ResourceNotFound("page", pageID).WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return failurecause.WithAttrs(domainerr.DependencyUnavailable("SpiceDB").WithCause(err), slog.String("page_id", pageID))
	}
	if !allowed {
		return domainerr.ResourceNotFound("page", pageID)
	}
	return nil
}

func requirePagePlatformAction(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	can policyv1.Can,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
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
		return domainerr.AdminRequired()
	}
	return nil
}

func requirePageCreate(ctx context.Context, checker authz.AuthorizationDecisionChecker) error {
	can, err := policyv1.Page.Create()
	if err != nil {
		return err
	}
	return requirePagePlatformAction(ctx, checker, can)
}

func requirePageList(ctx context.Context, checker authz.AuthorizationDecisionChecker) error {
	can, err := policyv1.Page.List()
	if err != nil {
		return err
	}
	return requirePagePlatformAction(ctx, checker, can)
}

// freshPageCreate is the access of Page creation: a denied fresh create
// check answers "admin access required".
func freshPageCreate() mutation.Access {
	return mutation.Locked(func(_ context.Context, _ Store, require mutation.Require) error {
		can, err := policyv1.Page.Create()
		if err != nil {
			return err
		}
		if err := require(can); err != nil {
			if mutation.IsDenied(err) {
				return domainerr.AdminRequired()
			}
			return err
		}
		return nil
	})
}

// lockedPage is the access of a Page mutation: the root is locked before
// the one exact action is checked, and a caller without it finds no Page.
func lockedPage(pageID string, action pageAction) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		return requireLockedPagePermission(ctx, store, require, pageID, action)
	})
}

func lockPageAuthorizationRoot(ctx context.Context, store Store, pageID string) error {
	if _, err := store.DocumentRoot(ctx, pageID, UpdateLock); err != nil {
		if errors.Is(err, ErrPageNotFound) {
			return domainerr.ResourceNotFound("page", pageID)
		}
		return err
	}
	return nil
}

func requireLockedPagePermission(
	ctx context.Context,
	store Store,
	require mutation.Require,
	pageID string,
	action pageAction,
) error {
	if err := lockPageAuthorizationRoot(ctx, store, pageID); err != nil {
		return err
	}
	_, err := authorizePagePermissionAfterRootLock(ctx, require, pageID, action)
	return err
}

// authorizePagePermissionAfterRootLock performs the current-principal fence
// and one exact SpiceDB decision through require after the caller has locked
// the Page root. Keeping this separate from requireLockedPagePermission lets
// aggregate-owned transactions avoid locking and authorizing the same Page
// twice.
func authorizePagePermissionAfterRootLock(
	ctx context.Context,
	require mutation.Require,
	pageID string,
	action pageAction,
) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return nil, domainerr.AuthenticationRequired()
	}
	can, err := action(pageID)
	if err != nil {
		return nil, domainerr.ResourceNotFound("page", pageID).WithCause(err)
	}
	if err := require(can); err != nil {
		if mutation.IsDenied(err) {
			return nil, domainerr.ResourceNotFound("page", pageID).WithCause(err)
		}
		return nil, err
	}
	return principal, nil
}
