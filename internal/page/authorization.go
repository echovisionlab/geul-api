package page

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type pageAction = auth.ResourceAction

// PolicyAuthority exposes the Page-owned root and active-principal fence to
// exact relation policy consumers without duplicating Page authorization.
type PolicyAuthority struct {
	checker CollaborationPermissionChecker
}

func NewPolicyAuthority(checker CollaborationPermissionChecker) *PolicyAuthority {
	return &PolicyAuthority{checker: checker}
}

func (a *PolicyAuthority) RequireLockedView(ctx context.Context, tx *gorm.DB, pageID string) error {
	if a == nil {
		return errs.DependencyUnavailable("Page policy access")
	}
	return requireLockedPagePermission(ctx, tx, a.checker, pageID, policyv1.Page.View)
}

func (a *PolicyAuthority) RequireLockedEdit(ctx context.Context, tx *gorm.DB, pageID string) error {
	if a == nil {
		return errs.DependencyUnavailable("Page policy access")
	}
	return requireLockedPagePermission(ctx, tx, a.checker, pageID, policyv1.Page.Edit)
}

func requirePagePermission(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	pageID string,
	action pageAction,
) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	can, err := action(pageID)
	if err != nil {
		return errs.NotFound("page", pageID)
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
		return errs.NotFound("page", pageID)
	}
	return nil
}

func requirePagePlatformAction(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	can policyv1.Can,
) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
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
		return errs.AdminRequired()
	}
	return nil
}

func requirePageCreate(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Page.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return requirePagePlatformAction(ctx, checker, can)
}

func requirePageList(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Page.List()
	if err != nil {
		return errs.Internal(err)
	}
	return requirePagePlatformAction(ctx, checker, can)
}

func requireLockedPageCreate(ctx context.Context, tx *gorm.DB, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Page.Create()
	if err != nil {
		return errs.Internal(err)
	}
	if err := identitystate.RequireFreshCan(ctx, tx, checker, can); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return errs.AdminRequired()
		}
		return err
	}
	return nil
}

func lockPageAuthorizationRoot(ctx context.Context, tx *gorm.DB, pageID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("page").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ?", pageID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("page", pageID)
		}
		return errs.Internal(err)
	}
	return nil
}

func requireLockedPagePermission(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	pageID string,
	action pageAction,
) error {
	if err := lockPageAuthorizationRoot(ctx, tx, pageID); err != nil {
		return err
	}
	_, err := authorizePagePermissionAfterRootLock(ctx, tx, checker, pageID, action)
	return err
}

// authorizePagePermissionAfterRootLock performs the current-principal fence
// and one exact SpiceDB decision after the caller has locked the Page root.
// Keeping this separate from requireLockedPagePermission lets aggregate-owned
// transactions avoid locking and authorizing the same Page twice.
func authorizePagePermissionAfterRootLock(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	pageID string,
	action pageAction,
) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return nil, errs.AuthenticationRequired()
	}
	can, err := action(pageID)
	if err != nil {
		return nil, errs.NotFound("page", pageID)
	}
	if err := identitystate.RequireFreshCan(ctx, tx, checker, can); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, errs.NotFound("page", pageID)
		}
		return nil, err
	}
	return principal, nil
}
