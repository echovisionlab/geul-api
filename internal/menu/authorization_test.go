package menu

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type menuPermissionCall struct {
	decision policyv1.AuthorizationDecision
}

type recordingMenuPermissionChecker struct {
	calls   []menuPermissionCall
	allowed bool
}

func (c *recordingMenuPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.calls = append(c.calls, menuPermissionCall{decision: decision})
	return c.allowed, nil
}

func TestCheckMenuPermissionUsesOneExactGeneratedAction(t *testing.T) {
	t.Parallel()
	menuID := uuid.NewString()
	identityID := auth.IdentityID(uuid.NewString())
	principal := &auth.UserInfo{
		IdentityID: identityID, MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
	for _, test := range []struct {
		name   string
		action menuAction
	}{
		{name: "view", action: policyv1.Menu.View},
		{name: "edit", action: policyv1.Menu.Edit},
		{name: "delete", action: policyv1.Menu.Delete},
		{name: "manage", action: policyv1.Menu.Manage},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			checker := &recordingMenuPermissionChecker{allowed: true}
			require.NoError(t, checkMenuPermission(t.Context(), checker, menuID, test.action, principal))
			require.Len(t, checker.calls, 1)
			can, err := test.action(menuID)
			require.NoError(t, err)
			require.Equal(t, can.EngineKey(), checker.calls[0].decision.EngineKey())
			require.Equal(t, can.Action().Name(), checker.calls[0].decision.Action().Name())
			require.Equal(t, can.Action().Permission(), checker.calls[0].decision.Action().Permission())
			require.Equal(t, identityID.String(), checker.calls[0].decision.Actor().AccountIdentityID())
		})
	}
}

func TestMenuGlobalBoundaryUsesOneGeneratedCreationAction(t *testing.T) {
	t.Parallel()
	identityID := auth.IdentityID(uuid.NewString())
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: identityID, MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	checker := &recordingMenuPermissionChecker{allowed: true}
	can, err := policyv1.Menu.Create()
	require.NoError(t, err)
	require.NoError(t, requireMenuGlobalCan(ctx, checker, can))
	require.Len(t, checker.calls, 1)
	require.Equal(t, can.EngineKey(), checker.calls[0].decision.EngineKey())
	require.Equal(t, can.Action().Name(), checker.calls[0].decision.Action().Name())
	require.Equal(t, can.Action().Permission(), checker.calls[0].decision.Action().Permission())
	require.Equal(t, identityID.String(), checker.calls[0].decision.Actor().AccountIdentityID())
}

func TestMenuAIDocumentSelectsOneExactActionPerCommand(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		operations []AIDocumentOperation
		want       menuAction
	}{
		{operations: []AIDocumentOperation{{Kind: AIDocumentSetItemField, ItemID: "item", Field: "label"}}, want: policyv1.Menu.Edit},
		{operations: []AIDocumentOperation{{Kind: AIDocumentCreateTranslation}}, want: policyv1.Menu.Edit},
		{operations: []AIDocumentOperation{
			{Kind: AIDocumentSetName},
			{Kind: AIDocumentMoveItem, ItemID: "item", ParentID: "menu"},
		}, want: policyv1.Menu.Manage},
	} {
		gotAction, err := menuAIDocumentAction(test.operations).authorizationAction()
		require.NoError(t, err)
		wantCan, err := test.want("menu-action-test")
		require.NoError(t, err)
		gotCan, err := gotAction("menu-action-test")
		require.NoError(t, err)
		require.Equal(t, wantCan.Action().Name(), gotCan.Action().Name())
		require.Equal(t, wantCan.Action().Permission(), gotCan.Action().Permission())
	}
}

func TestMenuUpdateSelectsEditForValuesAndManageForTopology(t *testing.T) {
	t.Parallel()
	currentItems := []model.MenuItem{{ID: "home", Label: "Home", LinkType: "custom"}}
	currentJSON, err := json.Marshal(currentItems)
	require.NoError(t, err)
	current := model.Menu{ID: "menu-action-test", Items: currentJSON}
	request := &managev1.UpdateMenuRequest{Items: &managev1.MenuItemsUpdate{}}

	labelOnly, err := json.Marshal([]model.MenuItem{{ID: "home", Label: "Start", LinkType: "custom"}})
	require.NoError(t, err)
	labelAction := menuUpdateAction(
		current,
		request,
		menuUpdate{fields: map[string]any{"items": json.RawMessage(labelOnly)}},
	)
	labelCan, err := labelAction(current.ID)
	require.NoError(t, err)
	expectedEdit, err := policyv1.Menu.Edit(current.ID)
	require.NoError(t, err)
	require.Equal(t, expectedEdit.Action().Name(), labelCan.Action().Name())
	require.Equal(t, expectedEdit.Action().Permission(), labelCan.Action().Permission())

	structural, err := json.Marshal([]model.MenuItem{
		{ID: "home", Label: "Home", LinkType: "custom"},
		{ID: "about", Label: "About", LinkType: "custom"},
	})
	require.NoError(t, err)
	structuralAction := menuUpdateAction(
		current,
		request,
		menuUpdate{fields: map[string]any{"items": json.RawMessage(structural)}},
	)
	structuralCan, err := structuralAction(current.ID)
	require.NoError(t, err)
	expectedManage, err := policyv1.Menu.Manage(current.ID)
	require.NoError(t, err)
	require.Equal(t, expectedManage.Action().Name(), structuralCan.Action().Name())
	require.Equal(t, expectedManage.Action().Permission(), structuralCan.Action().Permission())
}
