package label

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type recordingLabelPermissionChecker struct {
	allowed map[string]bool
	calls   []policyv1.AuthorizationDecision
}

func (c *recordingLabelPermissionChecker) Can(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
	c.calls = append(c.calls, decision)
	return c.allowed[decision.Action().Permission()], nil
}

func (c *recordingLabelPermissionChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	_ policyv1.Can,
) (bool, error) {
	return true, nil
}

func TestRequireLabelPermissionChecksExactActionOnce(t *testing.T) {
	labelID := "33333333-3333-4333-8333-333333333333"
	publish, err := policyv1.Label.Publish(labelID)
	require.NoError(t, err)
	checker := &recordingLabelPermissionChecker{allowed: map[string]bool{publish.Action().Permission(): true}}
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		MemberID:      auth.MemberID("22222222-2222-4222-8222-222222222222"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})

	require.NoError(t, requireLabelPermission(ctx, checker, labelID, policyv1.Label.Publish))
	require.Len(t, checker.calls, 1)
	require.Equal(t, "label", checker.calls[0].Resource().Type())
	require.Equal(t, labelID, checker.calls[0].Resource().ID())
	require.Equal(t, publish.Action().Permission(), checker.calls[0].Action().Permission())
}

func TestRequireLabelGlobalActionsUseDomainCatalog(t *testing.T) {
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})
	for _, test := range []struct {
		name       string
		actionName string
		require    func(context.Context, labelPermissionChecker) error
	}{
		{name: "create", actionName: "label.create", require: requireLabelCreate},
		{name: "list", actionName: "label.list", require: requireLabelList},
	} {
		t.Run(test.name, func(t *testing.T) {
			admin, err := policyv1.Platform.IsAdmin()
			require.NoError(t, err)
			checker := &recordingLabelPermissionChecker{allowed: map[string]bool{admin.Action().Permission(): true}}
			require.NoError(t, test.require(ctx, checker))
			require.Len(t, checker.calls, 1)
			decision := checker.calls[0]
			require.Equal(t, "platform", decision.Resource().Type())
			require.Equal(t, "global", decision.Resource().ID())
			require.Equal(t, test.actionName, decision.Action().Name())
			require.Equal(t, admin.Action().Permission(), decision.Action().Permission())
		})
	}
}

func TestLabelAllowedActionsChecksEachGeneratedPermissionOnce(t *testing.T) {
	labelID := "33333333-3333-4333-8333-333333333333"
	allowedActions := []func(string) (policyv1.Can, error){
		policyv1.Label.Edit,
		policyv1.Label.ManageShareLinks,
		policyv1.Label.Delete,
		policyv1.Label.ManageParticipants,
		policyv1.Label.Publish,
	}
	allowed := make(map[string]bool, len(allowedActions))
	for _, action := range allowedActions {
		can, err := action(labelID)
		require.NoError(t, err)
		allowed[can.Action().Permission()] = true
	}
	checker := &recordingLabelPermissionChecker{allowed: allowed}
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		MemberID:      auth.MemberID("22222222-2222-4222-8222-222222222222"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})

	actions, err := labelAllowedActions(ctx, checker, labelID, managev1.LabelStatus_LABEL_STATUS_DRAFT.String())
	require.NoError(t, err)
	require.Equal(t, []managev1.LabelAction{
		managev1.LabelAction_LABEL_ACTION_EDIT,
		managev1.LabelAction_LABEL_ACTION_MANAGE_SHARE_LINKS,
		managev1.LabelAction_LABEL_ACTION_DELETE,
		managev1.LabelAction_LABEL_ACTION_MANAGE_PARTICIPANTS,
		managev1.LabelAction_LABEL_ACTION_PUBLISH,
	}, actions)
	expectedPermissions := make([]string, 0, len(allowedActions)+1)
	for _, action := range allowedActions {
		can, err := action(labelID)
		require.NoError(t, err)
		expectedPermissions = append(expectedPermissions, can.Action().Permission())
	}
	removeOwner, err := policyv1.Label.RemoveOwner(labelID)
	require.NoError(t, err)
	expectedPermissions = append(expectedPermissions, removeOwner.Action().Permission())
	require.Equal(t, expectedPermissions, labelDecisionPermissions(checker.calls))
}

func labelDecisionPermissions(decisions []policyv1.AuthorizationDecision) []string {
	permissions := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		permissions = append(permissions, decision.Action().Permission())
	}
	return permissions
}
