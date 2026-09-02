package page

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestPagePermissionMapUsesGeneratedActions(t *testing.T) {
	t.Parallel()
	pageID := "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		action     pageAction
		actionName string
	}{
		{policyv1.Page.View, "view"},
		{policyv1.Page.Edit, "edit"},
		{policyv1.Page.Delete, "delete"},
		{policyv1.Page.Publish, "publish"},
		{policyv1.Page.Manage, "manage"},
		{policyv1.Page.ManageShareLinks, "manage_share_links"},
	}
	for _, test := range tests {
		can, err := test.action(pageID)
		require.NoError(t, err)
		require.Equal(t, "page", can.Resource().Type())
		require.Equal(t, pageID, can.Resource().ID())
		require.Equal(t, test.actionName, can.Action().Name())
		require.Equal(t, test.actionName, can.Action().Permission())
	}
}

type pageAuthorizationRecordingChecker struct {
	calls    int
	decision policyv1.AuthorizationDecision
}

func (c *pageAuthorizationRecordingChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.calls++
	c.decision = decision
	return true, nil
}

func (c *pageAuthorizationRecordingChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	_ policyv1.Can,
) (bool, error) {
	return true, nil
}

func TestRequirePagePermissionUsesOneExactObjectDecision(t *testing.T) {
	t.Parallel()
	pageID := "11111111-1111-4111-8111-111111111111"
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    "22222222-2222-4222-8222-222222222222",
		SessionID:     "page-authorization-test-session",
		Authenticated: true,
	})
	checker := &pageAuthorizationRecordingChecker{}

	if err := requirePagePermission(ctx, checker, pageID, policyv1.Page.ManageShareLinks); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 {
		t.Fatalf("permission checks = %d, want exactly 1", checker.calls)
	}
	if checker.decision.Resource().Type() != "page" || checker.decision.Resource().ID() != pageID {
		t.Fatalf("resource = %s:%s, want Page %s", checker.decision.Resource().Type(), checker.decision.Resource().ID(), pageID)
	}
	if checker.decision.Action().Name() != "manage_share_links" || checker.decision.Action().Permission() != "manage_share_links" {
		t.Fatalf("action = %q/%q, want manage_share_links", checker.decision.Action().Name(), checker.decision.Action().Permission())
	}
	if checker.decision.Actor().AccountIdentityID() != auth.GetUser(ctx).IdentityID.String() {
		t.Fatalf("actor = %q, want %q", checker.decision.Actor().AccountIdentityID(), auth.GetUser(ctx).IdentityID)
	}
}

func TestPagePlatformActionsKeepCreateAndListDistinct(t *testing.T) {
	t.Parallel()
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    "22222222-2222-4222-8222-222222222222",
		SessionID:     "page-platform-authorization-test-session",
		Authenticated: true,
	})
	tests := []struct {
		name   string
		invoke func(context.Context, CollaborationPermissionChecker) error
		action string
	}{
		{name: "create", invoke: requirePageCreate, action: "page.create"},
		{name: "list", invoke: requirePageList, action: "page.list"},
	}
	for _, test := range tests {
		checker := &pageAuthorizationRecordingChecker{}
		require.NoError(t, test.invoke(ctx, checker))
		require.Equal(t, 1, checker.calls)
		require.Equal(t, "platform", checker.decision.Resource().Type())
		require.Equal(t, "global", checker.decision.Resource().ID())
		require.Equal(t, test.action, checker.decision.Action().Name())
		require.Equal(t, "is_admin", checker.decision.Action().Permission())
	}
}
