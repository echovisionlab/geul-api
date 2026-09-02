package work

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
	if a == nil {
		return errs.DependencyUnavailable("Work policy access")
	}
	_, err := requireLockedWorkPermission(ctx, tx, a.checker, workID, policyv1.Work.View, workAuthorizationRead)
	return err
}

func (a *PolicyAuthority) RequireLockedEdit(ctx context.Context, tx *gorm.DB, workID string) error {
	if a == nil {
		return errs.DependencyUnavailable("Work policy access")
	}
	_, err := requireLockedWorkPermission(ctx, tx, a.checker, workID, policyv1.Work.Edit, workAuthorizationMutation)
	return err
}

const (
	workAuthorizationRead workAuthorizationUse = iota
	workAuthorizationMutation
)

func requireWorkGlobalAction(ctx context.Context, checker CollaborationPermissionChecker, can policyv1.Can) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
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
		return errs.AdminRequired()
	}
	return nil
}

func requireWorkCreate(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Work.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return requireWorkGlobalAction(ctx, checker, can)
}

func requireWorkList(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Work.List()
	if err != nil {
		return errs.Internal(err)
	}
	return requireWorkGlobalAction(ctx, checker, can)
}

func requireLockedWorkCreate(ctx context.Context, tx *gorm.DB, checker *auth.SpiceDBClient) error {
	can, err := policyv1.Work.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return identitystate.RequireFreshAdminCan(ctx, tx, checker, can)
}

func workLifecycleAction(status string, normal workAction, use workAuthorizationUse) workAction {
	if status != managev1.WorkStatus_WORK_STATUS_ARCHIVED.String() {
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
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := action(workID)
	if err != nil {
		return errs.NotFound("work", workID)
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
		return errs.NotFound("work", workID)
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
	return requireWorkPermissionForCurrentActor(ctx, checker, work.ID, workLifecycleAction(string(work.Status), normal, use))
}

func lockWorkAuthorizationRoot(ctx context.Context, tx *gorm.DB, workID string) (model.Work, error) {
	var work model.Work
	if err := tx.WithContext(ctx).Table("work").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").Where("id = ?", workID).Take(&work).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Work{}, errs.NotFound("work", workID)
		}
		return model.Work{}, errs.Internal(err)
	}
	return work, nil
}

func requireLockedWorkPermission(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
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
		return model.Work{}, errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return model.Work{}, errs.Internal(err)
	}
	if !active {
		return model.Work{}, errs.NotFound("work", workID)
	}
	if err := requireWorkPermission(ctx, checker, work, normal, use); err != nil {
		return model.Work{}, err
	}
	return work, nil
}
