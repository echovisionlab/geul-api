package work

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type workAuthorizationUse uint8
type workAction = auth.ResourceAction

// PolicyAuthority exposes the Work-owned lifecycle and active-principal fence
// to relation policy consumers without duplicating Work authorization rules.
type PolicyAuthority struct {
	checker CollaborationPermissionChecker
}

func NewPolicyAuthority(checker CollaborationPermissionChecker) *PolicyAuthority {
	return &PolicyAuthority{checker: checker}
}

func (a *PolicyAuthority) RequireLockedView(ctx context.Context, tx *gorm.DB, workID string) error {
	_, err := requireLockedWorkPermission(ctx, tx, freshRequire(ctx, tx, a.checker), workID, policyv1.Work.View, workAuthorizationRead)
	return err
}

func (a *PolicyAuthority) RequireLockedEdit(ctx context.Context, tx *gorm.DB, workID string) error {
	_, err := requireLockedWorkPermission(ctx, tx, freshRequire(ctx, tx, a.checker), workID, policyv1.Work.Edit, workAuthorizationMutation)
	return err
}

const (
	workAuthorizationRead workAuthorizationUse = iota
	workAuthorizationMutation
)

func requireWorkGlobalAction(ctx context.Context, checker CollaborationPermissionChecker, can policyv1.Can) error {
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

func requireWorkCreate(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Work.Create()
	if err != nil {
		return err
	}
	return requireWorkGlobalAction(ctx, checker, can)
}

func requireWorkList(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Work.List()
	if err != nil {
		return err
	}
	return requireWorkGlobalAction(ctx, checker, can)
}

func workLifecycleAction(status model.WorkStatus, normal workAction, use workAuthorizationUse) workAction {
	if status != model.WorkStatusArchived {
		return normal
	}
	if use == workAuthorizationRead {
		return policyv1.Work.ViewArchived
	}
	return policyv1.Work.EditArchived
}

func requireWorkPermissionForCurrentActor(
	ctx context.Context,
	checker CollaborationPermissionChecker,
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
	checker CollaborationPermissionChecker,
	work model.Work,
	normal workAction,
	use workAuthorizationUse,
) error {
	return requireWorkPermissionForCurrentActor(ctx, checker, work.ID, workLifecycleAction(work.Status, normal, use))
}

func lockWorkAuthorizationRoot(ctx context.Context, tx *gorm.DB, workID string) (model.Work, error) {
	var work model.Work
	if err := tx.WithContext(ctx).Table("work").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status").Where("id = ?", workID).Take(&work).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Work{}, domainerr.ResourceNotFound("work", workID)
		}
		return model.Work{}, err
	}
	return work, nil
}

func freshRequire(ctx context.Context, tx *gorm.DB, checker authz.AuthorizationDecisionChecker) mutation.Require {
	return func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(ctx, tx, checker, can)
	}
}

// requireLockedWorkPermission locks the Work root and the principal, then
// requires the one action the locked lifecycle selects from normal. A
// caller without it, inactive or denied, finds no Work.
func requireLockedWorkPermission(
	ctx context.Context,
	tx *gorm.DB,
	require mutation.Require,
	workID string,
	normal workAction,
	use workAuthorizationUse,
) (model.Work, error) {
	work, err := lockWorkAuthorizationRoot(ctx, tx, workID)
	if err != nil {
		return model.Work{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return model.Work{}, domainerr.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return model.Work{}, err
	}
	if !active {
		return model.Work{}, domainerr.ResourceNotFound("work", workID)
	}
	can, err := workLifecycleAction(work.Status, normal, use)(workID)
	if err != nil {
		return model.Work{}, domainerr.ResourceNotFound("work", workID).WithCause(err)
	}
	if err := require(can); err != nil {
		if mutation.IsDenied(err) {
			return model.Work{}, domainerr.ResourceNotFound("work", workID).WithCause(err)
		}
		return model.Work{}, err
	}
	return work, nil
}

// lockedWork is the access of a Work mutation: the root is locked before
// the one action its lifecycle requires is checked. locked, when set,
// receives the locked root.
func lockedWork(workID string, normal workAction, locked *model.Work) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		work, err := requireLockedWorkPermission(ctx, tx, require, workID, normal, workAuthorizationMutation)
		if locked != nil {
			*locked = work
		}
		return err
	})
}
