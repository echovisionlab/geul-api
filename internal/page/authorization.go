package page

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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
		return domainerr.DependencyUnavailable("Page policy access")
	}
	if err := requireLockedPagePermission(ctx, tx, a.checker, pageID, policyv1.Page.View); err != nil {
		return err
	}
	reason, err := pageaccess.EvaluateStored(ctx, tx, a.checker, pageID)
	if err != nil {
		return err
	}
	if reason != commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED {
		return domainerr.Denied("you do not have permission to access this page")
	}
	return nil
}

func (a *PolicyAuthority) RequireLockedEdit(ctx context.Context, tx *gorm.DB, pageID string) error {
	if a == nil {
		return domainerr.DependencyUnavailable("Page policy access")
	}
	return requireLockedPagePermission(ctx, tx, a.checker, pageID, policyv1.Page.Edit)
}

// requirePagePermission checks action on the Page outside a transaction. A
// caller without it finds no Page.
func requirePagePermission(
	ctx context.Context,
	checker CollaborationPermissionChecker,
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
	checker CollaborationPermissionChecker,
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

func requirePageCreate(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Page.Create()
	if err != nil {
		return err
	}
	return requirePagePlatformAction(ctx, checker, can)
}

func requirePageList(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Page.List()
	if err != nil {
		return err
	}
	return requirePagePlatformAction(ctx, checker, can)
}

// freshPageCreate is the access of Page creation: a denied fresh create
// check answers "admin access required".
func (s *Service) freshPageCreate() mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		can, err := policyv1.Page.Create()
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshCan(ctx, tx, s.permissions, can); err != nil {
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
func (s *Service) lockedPage(pageID string, action pageAction) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		return requireLockedPagePermission(ctx, tx, s.permissions, pageID, action)
	})
}

func lockPageAuthorizationRoot(ctx context.Context, tx *gorm.DB, pageID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("page").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").Where("id = ?", pageID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("page", pageID)
		}
		return err
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
		return nil, domainerr.AuthenticationRequired()
	}
	can, err := action(pageID)
	if err != nil {
		return nil, domainerr.ResourceNotFound("page", pageID).WithCause(err)
	}
	if err := identitystate.RequireFreshCan(ctx, tx, checker, can); err != nil {
		if mutation.IsDenied(err) {
			return nil, domainerr.ResourceNotFound("page", pageID).WithCause(err)
		}
		return nil, err
	}
	return principal, nil
}
