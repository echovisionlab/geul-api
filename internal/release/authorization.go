package release

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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
		return notAuthenticated()
	}
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return authenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return adminRequired()
	}
	return nil
}

func requireReleaseCreate(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Release.Create()
	if err != nil {
		return err
	}
	return requireReleaseGlobalAction(ctx, checker, can)
}

func requireReleaseList(ctx context.Context, checker CollaborationPermissionChecker) error {
	can, err := policyv1.Release.List()
	if err != nil {
		return err
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
		return authenticationRequired()
	}
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	can, err := action.can(releaseID)
	if err != nil {
		return releaseNotFound(releaseID).WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return authenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return releaseNotFound(releaseID)
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
		return authenticationRequired()
	}
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	can, err := action.can(trackID)
	if err != nil {
		return domainerr.ResourceNotFound("track", trackID).WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return authenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return noPermission(can.Action().Permission(), "track")
	}
	return nil
}

// lockActive locks a principal's Identity through the identity fence and
// reports whether it is still active, onboarded and unbanned
// (identitystate.Fence.LockActivePrincipal).
type lockActive = func(*auth.UserInfo) (bool, error)

func requireActiveTrackAction(
	ctx context.Context,
	lock lockActive,
	checker CollaborationPermissionChecker,
	trackID string,
	action trackObjectAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return authenticationRequired()
	}
	active, err := lock(principal)
	if err != nil {
		return err
	}
	if !active {
		return domainerr.ResourceNotFound("track", trackID)
	}
	return requireTrackAction(ctx, checker, trackID, action)
}

// requireLockedReleaseAction is the mutation linearization point: the
// owning root and active principal are locked before exactly one object
// permission is checked for the requested action.
func requireLockedReleaseAction(
	ctx context.Context,
	store Store,
	lock lockActive,
	checker CollaborationPermissionChecker,
	releaseID string,
	action releaseObjectAction,
) error {
	if _, err := store.LockRelease(ctx, releaseID, UpdateLock); err != nil {
		return err
	}
	return requireActiveReleaseAction(ctx, lock, checker, releaseID, action)
}

func requireActiveReleaseAction(
	ctx context.Context,
	lock lockActive,
	checker CollaborationPermissionChecker,
	releaseID string,
	action releaseObjectAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return authenticationRequired()
	}
	active, err := lock(principal)
	if err != nil {
		return err
	}
	if !active {
		return releaseNotFound(releaseID)
	}
	if err := requireReleaseAction(ctx, checker, releaseID, action); err != nil {
		return err
	}
	return nil
}

// releaseAccess is the Access of a Release mutation: the caller's identity
// fence and the exact object permission, read inside the transaction. A
// denial answers not found, as the Release RPCs always have.
func (s *Service) releaseAccess(releaseID string, action releaseObjectAction) mutation.Access {
	return mutation.Fenced(func(ctx context.Context, _ Store, lock lockActive) error {
		return requireActiveReleaseAction(ctx, lock, s.spiceDB, releaseID, action)
	})
}

// trackAccess is the Access of a Track mutation.
func (s *Service) trackAccess(trackID string, action trackObjectAction) mutation.Access {
	return mutation.Fenced(func(ctx context.Context, _ Store, lock lockActive) error {
		return requireActiveTrackAction(ctx, lock, s.spiceDB, trackID, action)
	})
}
