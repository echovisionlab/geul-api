package release

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type releasePermissionCall struct {
	resourceType    string
	resourceID      string
	actionName      string
	permission      string
	actorIdentityID string
}

type recordingReleasePermissionChecker struct {
	allowed bool
	calls   []releasePermissionCall
}

func (c *recordingReleasePermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.calls = append(c.calls, releasePermissionCall{
		resourceType:    decision.Resource().Type(),
		resourceID:      decision.Resource().ID(),
		actionName:      decision.Action().Name(),
		permission:      decision.Action().Permission(),
		actorIdentityID: decision.Actor().AccountIdentityID(),
	})
	return c.allowed, nil
}

func TestRequireReleaseActionChecksOnlyExactTypedDecision(t *testing.T) {
	tests := []struct {
		name     string
		action   releaseObjectAction
		expected func(string) (policyv1.Can, error)
	}{
		{name: "view", action: releaseActionView, expected: policyv1.Release.View},
		{name: "edit", action: releaseActionEdit, expected: policyv1.Release.Edit},
		{name: "delete", action: releaseActionDelete, expected: policyv1.Release.Delete},
		{name: "publish", action: releaseActionPublish, expected: policyv1.Release.Publish},
		{name: "manage", action: releaseActionManage, expected: policyv1.Release.Manage},
		{name: "manage_share_links", action: releaseActionManageShareLinks, expected: policyv1.Release.ManageShareLinks},
	}
	releaseID := uuid.NewString()
	identityID := auth.IdentityID(uuid.NewString())
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: identityID, SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker := &recordingReleasePermissionChecker{allowed: true}
			require.NoError(t, requireReleaseAction(ctx, checker, releaseID, test.action))
			require.Len(t, checker.calls, 1)
			expected, err := test.expected(releaseID)
			require.NoError(t, err)
			call := checker.calls[0]
			require.Equal(t, expected.Resource().Type(), call.resourceType)
			require.Equal(t, expected.Resource().ID(), call.resourceID)
			require.Equal(t, expected.Action().Name(), call.actionName)
			require.Equal(t, expected.Action().Permission(), call.permission)
			require.Equal(t, identityID.String(), call.actorIdentityID)
		})
	}
}

func TestRequireReleaseActionDoesNotFallbackWhenDenied(t *testing.T) {
	releaseID := uuid.NewString()
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	})
	checker := &recordingReleasePermissionChecker{}

	require.Error(t, requireReleaseAction(ctx, checker, releaseID, releaseActionEdit))
	require.Len(t, checker.calls, 1)
	expected, err := policyv1.Release.Edit(releaseID)
	require.NoError(t, err)
	require.Equal(t, expected.Action().Name(), checker.calls[0].actionName)
	require.Equal(t, expected.Action().Permission(), checker.calls[0].permission)
}

func TestRequireReleaseGlobalActionsUseDomainCatalog(t *testing.T) {
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	})
	for _, test := range []struct {
		name     string
		require  func(context.Context, CollaborationPermissionChecker) error
		expected func() (policyv1.Can, error)
	}{
		{name: "create", require: requireReleaseCreate, expected: policyv1.Release.Create},
		{name: "list", require: requireReleaseList, expected: policyv1.Release.List},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &recordingReleasePermissionChecker{allowed: true}
			require.NoError(t, test.require(ctx, checker))
			require.Len(t, checker.calls, 1)
			expected, err := test.expected()
			require.NoError(t, err)
			call := checker.calls[0]
			require.Equal(t, expected.Resource().Type(), call.resourceType)
			require.Equal(t, expected.Resource().ID(), call.resourceID)
			require.Equal(t, expected.Action().Name(), call.actionName)
			require.Equal(t, expected.Action().Permission(), call.permission)
		})
	}
}

func TestRequireTrackActionChecksOnlyExactActionPermission(t *testing.T) {
	trackID := uuid.NewString()
	identityID := auth.IdentityID(uuid.NewString())
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: identityID, MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	for _, test := range []struct {
		name     string
		action   trackObjectAction
		expected func(string) (policyv1.Can, error)
	}{
		{name: "edit", action: trackActionEdit, expected: policyv1.Track.Edit},
		{name: "delete", action: trackActionDelete, expected: policyv1.Track.Delete},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &recordingReleasePermissionChecker{allowed: true}
			require.NoError(t, requireTrackAction(ctx, checker, trackID, test.action))
			require.Len(t, checker.calls, 1)
			expected, err := test.expected(trackID)
			require.NoError(t, err)
			call := checker.calls[0]
			require.Equal(t, expected.Resource().Type(), call.resourceType)
			require.Equal(t, expected.Resource().ID(), call.resourceID)
			require.Equal(t, expected.Action().Name(), call.actionName)
			require.Equal(t, expected.Action().Permission(), call.permission)
			require.Equal(t, identityID.String(), call.actorIdentityID)
		})
	}
}

func TestRequireReleaseCollaborationChecksOnlyEdit(t *testing.T) {
	releaseID := uuid.NewString()
	principal := &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	}
	checker := &recordingReleasePermissionChecker{allowed: true}

	require.NoError(t, RequireCollaborationEdit(
		t.Context(), checker,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE,
		releaseID,
		principal,
	))
	require.Len(t, checker.calls, 1)
	expected, err := policyv1.Release.Edit(releaseID)
	require.NoError(t, err)
	call := checker.calls[0]
	require.Equal(t, expected.Resource().Type(), call.resourceType)
	require.Equal(t, expected.Resource().ID(), call.resourceID)
	require.Equal(t, expected.Action().Name(), call.actionName)
	require.Equal(t, expected.Action().Permission(), call.permission)
	require.Equal(t, principal.IdentityID.String(), call.actorIdentityID)
}
