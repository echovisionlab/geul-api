package series

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type recordingSeriesPermissionChecker struct {
	calls   []policyv1.AuthorizationDecision
	allowed bool
	err     error
}

func (c *recordingSeriesPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.calls = append(c.calls, decision)
	return c.allowed, c.err
}

func TestCheckSeriesActionMasksPrivateDenialAndPreservesDependencyFailure(t *testing.T) {
	t.Parallel()
	seriesID := uuid.NewString()
	principal := &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true, Onboarded: true,
	}

	t.Run("denied is not found", func(t *testing.T) {
		t.Parallel()
		checker := &recordingSeriesPermissionChecker{}
		err := checkSeriesPermission(t.Context(), checker, seriesID, policyv1.PostSeries.Manage, principal)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		require.Len(t, checker.calls, 1)
	})

	t.Run("dependency failure remains unavailable", func(t *testing.T) {
		t.Parallel()
		checker := &recordingSeriesPermissionChecker{err: errors.New("SpiceDB unavailable")}
		err := checkSeriesPermission(t.Context(), checker, seriesID, policyv1.PostSeries.Manage, principal)
		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		require.Len(t, checker.calls, 1)
	})
}

func (*recordingSeriesPermissionChecker) LookupResources(
	context.Context,
	policyv1.ResourceLookup,
	policyv1.Actor,
) ([]string, error) {
	return nil, nil
}

func TestCheckSeriesActionUsesOneExactGeneratedAction(t *testing.T) {
	t.Parallel()
	seriesID := uuid.NewString()
	identityID := auth.IdentityID(uuid.NewString())
	principal := &auth.UserInfo{
		IdentityID: identityID, MemberID: auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true, Onboarded: true,
	}
	actions := []struct {
		name   string
		action seriesAction
	}{
		{name: "view", action: policyv1.PostSeries.View},
		{name: "edit", action: policyv1.PostSeries.Edit},
		{name: "delete", action: policyv1.PostSeries.Delete},
		{name: "publish", action: policyv1.PostSeries.Publish},
		{name: "manage", action: policyv1.PostSeries.Manage},
		{name: "manage_participants", action: policyv1.PostSeries.ManageParticipants},
	}
	for _, test := range actions {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			checker := &recordingSeriesPermissionChecker{allowed: true}
			require.NoError(t, checkSeriesPermission(t.Context(), checker, seriesID, test.action, principal))
			require.Len(t, checker.calls, 1)
			decision := checker.calls[0]
			want, err := test.action(seriesID)
			require.NoError(t, err)
			require.Equal(t, want.EngineKey(), decision.EngineKey())
			require.Equal(t, identityID.String(), decision.Actor().AccountIdentityID())
		})
	}
}

func TestSeriesPlatformBoundaryUsesOneGlobalCreationAction(t *testing.T) {
	t.Parallel()
	identityID := auth.IdentityID(uuid.NewString())
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: identityID, MemberID: auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true, Onboarded: true,
	})
	checker := &recordingSeriesPermissionChecker{allowed: true}
	can, err := policyv1.PostSeries.Create()
	require.NoError(t, err)
	require.NoError(t, requireSeriesPlatformPermission(ctx, checker, can))
	require.Len(t, checker.calls, 1)
	require.Equal(t, can.EngineKey(), checker.calls[0].EngineKey())
	require.Equal(t, identityID.String(), checker.calls[0].Actor().AccountIdentityID())
}

func TestSeriesUpdateSelectsEditOrPublishAction(t *testing.T) {
	t.Parallel()
	seriesID := uuid.NewString()
	got, err := seriesUpdateAction(&managev1.UpdateSeriesRequest{})(seriesID)
	require.NoError(t, err)
	want, err := policyv1.PostSeries.Edit(seriesID)
	require.NoError(t, err)
	require.Equal(t, want.EngineKey(), got.EngineKey())
	status := managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String()
	got, err = seriesUpdateAction(&managev1.UpdateSeriesRequest{Status: &status})(seriesID)
	require.NoError(t, err)
	want, err = policyv1.PostSeries.Publish(seriesID)
	require.NoError(t, err)
	require.Equal(t, want.EngineKey(), got.EngineKey())
}
