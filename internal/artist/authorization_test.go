package artist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type recordingArtistPermissionChecker struct {
	allowed   map[string]bool
	calls     []policyv1.AuthorizationDecision
	decisions []policyv1.AuthorizationDecision
}

func (c *recordingArtistPermissionChecker) Can(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
	c.calls = append(c.calls, decision)
	c.decisions = append(c.decisions, decision)
	return c.allowed[decision.Action().Permission()], nil
}

func TestRequireArtistPermissionChecksExactActionOnce(t *testing.T) {
	artistID := "33333333-3333-4333-8333-333333333333"
	delete, err := policyv1.Artist.Delete(artistID)
	require.NoError(t, err)
	checker := &recordingArtistPermissionChecker{allowed: map[string]bool{delete.Action().Permission(): true}}
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		MemberID:      auth.MemberID("22222222-2222-4222-8222-222222222222"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})

	require.NoError(t, requireArtistPermission(ctx, checker, artistID, policyv1.Artist.Delete))
	require.Len(t, checker.calls, 1)
	require.Equal(t, "artist", checker.calls[0].Resource().Type())
	require.Equal(t, artistID, checker.calls[0].Resource().ID())
	require.Equal(t, delete.Action().Permission(), checker.calls[0].Action().Permission())
}

func TestRequireArtistGlobalActionsUseDomainCatalog(t *testing.T) {
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})
	for _, test := range []struct {
		name       string
		actionName string
		require    func(context.Context, artistPermissionChecker) error
	}{
		{name: "create", actionName: "artist.create", require: requireArtistCreate},
		{name: "list", actionName: "artist.list", require: requireArtistList},
	} {
		t.Run(test.name, func(t *testing.T) {
			admin, err := policyv1.Platform.IsAdmin()
			require.NoError(t, err)
			checker := &recordingArtistPermissionChecker{allowed: map[string]bool{admin.Action().Permission(): true}}
			require.NoError(t, test.require(ctx, checker))
			require.Len(t, checker.decisions, 1)
			decision := checker.decisions[0]
			require.Equal(t, "platform", decision.Resource().Type())
			require.Equal(t, "global", decision.Resource().ID())
			require.Equal(t, test.actionName, decision.Action().Name())
			require.Equal(t, admin.Action().Permission(), decision.Action().Permission())
		})
	}
}

func TestArtistAllowedActionsChecksEachGeneratedPermissionOnce(t *testing.T) {
	artistID := "33333333-3333-4333-8333-333333333333"
	allowedActions := []func(string) (policyv1.Can, error){
		policyv1.Artist.Edit,
		policyv1.Artist.ManageShareLinks,
		policyv1.Artist.Delete,
		policyv1.Artist.ManageParticipants,
		policyv1.Artist.Publish,
	}
	allowed := make(map[string]bool, len(allowedActions))
	for _, action := range allowedActions {
		can, err := action(artistID)
		require.NoError(t, err)
		allowed[can.Action().Permission()] = true
	}
	checker := &recordingArtistPermissionChecker{allowed: allowed}
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		MemberID:      auth.MemberID("22222222-2222-4222-8222-222222222222"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})

	actions, err := artistAllowedActions(ctx, checker, artistID, managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String())
	require.NoError(t, err)
	require.Equal(t, []managev1.ArtistAction{
		managev1.ArtistAction_ARTIST_ACTION_EDIT,
		managev1.ArtistAction_ARTIST_ACTION_MANAGE_SHARE_LINKS,
		managev1.ArtistAction_ARTIST_ACTION_DELETE,
		managev1.ArtistAction_ARTIST_ACTION_MANAGE_PARTICIPANTS,
		managev1.ArtistAction_ARTIST_ACTION_PUBLISH,
	}, actions)
	expectedPermissions := make([]string, 0, len(allowedActions)+1)
	for _, action := range allowedActions {
		can, err := action(artistID)
		require.NoError(t, err)
		expectedPermissions = append(expectedPermissions, can.Action().Permission())
	}
	removeOwner, err := policyv1.Artist.RemoveOwner(artistID)
	require.NoError(t, err)
	expectedPermissions = append(expectedPermissions, removeOwner.Action().Permission())
	require.Equal(t, expectedPermissions, artistDecisionPermissions(checker.calls))
}

func artistDecisionPermissions(decisions []policyv1.AuthorizationDecision) []string {
	permissions := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		permissions = append(permissions, decision.Action().Permission())
	}
	return permissions
}
