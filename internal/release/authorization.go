package release

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

type releaseObjectAction uint8

const (
	releaseActionView releaseObjectAction = iota + 1
	releaseActionEdit
	releaseActionDelete
	releaseActionPublish
	releaseActionManage
	releaseActionManageShareLinks
)

func (action releaseObjectAction) can(releaseID string) (policyv1.Can, error) {
	switch action {
	case releaseActionView:
		return policyv1.Release.View(releaseID)
	case releaseActionEdit:
		return policyv1.Release.Edit(releaseID)
	case releaseActionDelete:
		return policyv1.Release.Delete(releaseID)
	case releaseActionPublish:
		return policyv1.Release.Publish(releaseID)
	case releaseActionManage:
		return policyv1.Release.Manage(releaseID)
	case releaseActionManageShareLinks:
		return policyv1.Release.ManageShareLinks(releaseID)
	default:
		return policyv1.Can{}, fmt.Errorf("unsupported Release authorization action %d", action)
	}
}

type trackObjectAction uint8

const (
	trackActionEdit trackObjectAction = iota + 1
	trackActionDelete
)

func (action trackObjectAction) can(trackID string) (policyv1.Can, error) {
	switch action {
	case trackActionEdit:
		return policyv1.Track.Edit(trackID)
	case trackActionDelete:
		return policyv1.Track.Delete(trackID)
	default:
		return policyv1.Can{}, fmt.Errorf("unsupported Track authorization action %d", action)
	}
}

func requireReleaseGlobalAction(ctx context.Context, checker CollaborationPermissionChecker, can policyv1.Can) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.NotAuthenticated()
	}
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

func requireReleaseCreate(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Release.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return requireReleaseGlobalAction(ctx, checker, can)
}

func requireReleaseList(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Release.List()
	if err != nil {
		return errs.Internal(err)
	}
	return requireReleaseGlobalAction(ctx, checker, can)
}

func requireReleaseAction(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	releaseID string,
	action releaseObjectAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" {
		return errs.AuthenticationRequired()
	}
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := action.can(releaseID)
	if err != nil {
		return errs.NotFound("release", releaseID)
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
		return errs.NotFound("release", releaseID)
	}
	return nil
}

func requireTrackAction(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	trackID string,
	action trackObjectAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" {
		return errs.AuthenticationRequired()
	}
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := action.can(trackID)
	if err != nil {
		return errs.NotFound("track", trackID)
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
		return errs.NoPermission(can.Action().Permission(), "track")
	}
	return nil
}

func requireActiveTrackAction(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	trackID string,
	action trackObjectAction,
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
		return errs.NotFound("track", trackID)
	}
	return requireTrackAction(ctx, checker, trackID, action)
}

// requireLockedReleaseAction is the mutation linearization point: the
// owning root and active principal are locked before exactly one object
// permission is checked for the requested action.
func requireLockedReleaseAction(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	releaseID string,
	action releaseObjectAction,
) (*model.Release, error) {
	root, err := lockReleaseForUpdate(ctx, tx, releaseID)
	if err != nil {
		return nil, err
	}
	if err := requireActiveReleaseAction(ctx, tx, checker, releaseID, action); err != nil {
		return nil, err
	}
	return root, nil
}

func requireActiveReleaseAction(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	releaseID string,
	action releaseObjectAction,
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
		return errs.NotFound("release", releaseID)
	}
	if err := requireReleaseAction(ctx, checker, releaseID, action); err != nil {
		return err
	}
	return nil
}
