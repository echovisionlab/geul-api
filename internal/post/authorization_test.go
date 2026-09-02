package post

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestPostAllowedActionsKeepArchivedAuthorReadOnlyAndAdminEditable(t *testing.T) {
	const postID = "33333333-3333-4333-8333-333333333333"
	status := model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String())
	viewArchived, err := policyv1.Post.ViewArchived(postID)
	require.NoError(t, err)
	editArchived, err := policyv1.Post.EditArchived(postID)
	require.NoError(t, err)

	authorActions := postAllowedActions(postID, status, postAuthority(viewArchived))
	require.Equal(t, []managev1.PostAction{
		managev1.PostAction_POST_ACTION_VIEW_VERSIONS,
	}, authorActions)

	adminActions := postAllowedActions(postID, status, postAuthority(viewArchived, editArchived))
	require.Contains(t, adminActions, managev1.PostAction_POST_ACTION_REPUBLISH)
	require.NotContains(t, adminActions, managev1.PostAction_POST_ACTION_DELETE)
	require.Contains(t, adminActions, managev1.PostAction_POST_ACTION_EDIT)
	require.Contains(t, adminActions, managev1.PostAction_POST_ACTION_RESTORE_VERSION)
}

func TestPostAllowedActionsDoNotInferOrdinaryActionsFromEdit(t *testing.T) {
	const postID = "33333333-3333-4333-8333-333333333333"
	status := model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String())
	view, err := policyv1.Post.View(postID)
	require.NoError(t, err)
	edit, err := policyv1.Post.Edit(postID)
	require.NoError(t, err)
	actions := postAllowedActions(postID, status, postAuthority(view, edit))

	require.Contains(t, actions, managev1.PostAction_POST_ACTION_EDIT)
	require.NotContains(t, actions, managev1.PostAction_POST_ACTION_PUBLISH_NOW)
	require.NotContains(t, actions, managev1.PostAction_POST_ACTION_MANAGE_SHARE_LINKS)
	require.NotContains(t, actions, managev1.PostAction_POST_ACTION_MANAGE_COLLABORATORS)
}

func TestPostAuthorityIsBoundToExactPostResource(t *testing.T) {
	const checkedPostID = "33333333-3333-4333-8333-333333333333"
	const otherPostID = "44444444-4444-4444-8444-444444444444"
	checkedEdit, err := policyv1.Post.Edit(checkedPostID)
	require.NoError(t, err)
	otherEdit, err := policyv1.Post.Edit(otherPostID)
	require.NoError(t, err)

	authority := postAuthority(checkedEdit)
	require.True(t, authority.allows(checkedEdit))
	require.False(t, authority.allows(otherEdit))
	require.False(t, authority.allows(policyv1.Can{}))
}

func TestPostAllowedActionsSeparateAuthorRemovalFromParticipantManagement(t *testing.T) {
	const postID = "33333333-3333-4333-8333-333333333333"
	status := model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String())
	view, err := policyv1.Post.View(postID)
	require.NoError(t, err)
	manageParticipants, err := policyv1.Post.ManageParticipants(postID)
	require.NoError(t, err)
	removeAuthor, err := policyv1.Post.RemoveAuthor(postID)
	require.NoError(t, err)
	authorActions := postAllowedActions(postID, status, postAuthority(view, manageParticipants))
	require.Contains(t, authorActions, managev1.PostAction_POST_ACTION_ADD_AUTHOR)
	require.Contains(t, authorActions, managev1.PostAction_POST_ACTION_MANAGE_COLLABORATORS)
	require.NotContains(t, authorActions, managev1.PostAction_POST_ACTION_REMOVE_AUTHOR)

	adminActions := postAllowedActions(postID, status, postAuthority(view, manageParticipants, removeAuthor))
	require.Contains(t, adminActions, managev1.PostAction_POST_ACTION_REMOVE_AUTHOR)
}

func TestPostActionMapUsesGeneratedActions(t *testing.T) {
	t.Parallel()
	postID := "33333333-3333-4333-8333-333333333333"
	tests := []struct {
		action     postAction
		name       string
		permission string
	}{
		{policyv1.Post.View, "view", "view"},
		{policyv1.Post.ViewArchived, "view_archived", "view_archived"},
		{policyv1.Post.Edit, "edit", "edit"},
		{policyv1.Post.EditArchived, "edit_archived", "edit_archived"},
		{policyv1.Post.Delete, "delete", "delete"},
		{policyv1.Post.Publish, "publish", "publish"},
		{policyv1.Post.Manage, "manage", "manage"},
		{policyv1.Post.ManageParticipants, "manage_participants", "manage_participants"},
		{policyv1.Post.ManageShareLinks, "manage_share_links", "manage_share_links"},
		{policyv1.Post.RemoveAuthor, "remove_author", "platform_admin"},
	}
	for _, test := range tests {
		can, err := test.action(postID)
		require.NoError(t, err)
		require.Equal(t, "post", can.Resource().Type())
		require.Equal(t, postID, can.Resource().ID())
		require.Equal(t, test.name, can.Action().Name())
		require.Equal(t, test.permission, can.Action().Permission())
	}
}

func TestPostListUsesGeneratedDomainAction(t *testing.T) {
	t.Parallel()
	checker := &allowingPostPermissionChecker{allowed: true}
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    "11111111-1111-4111-8111-111111111111",
		SessionID:     "post-list-authorization-test-session",
		Authenticated: true,
	})

	require.NoError(t, requirePostList(ctx, checker))
	require.Len(t, checker.decisions, 1)
	decision := checker.decisions[0]
	require.Equal(t, "platform", decision.Resource().Type())
	require.Equal(t, "global", decision.Resource().ID())
	require.Equal(t, "post.list", decision.Action().Name())
	isAdmin, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	require.Equal(t, isAdmin.Action().Permission(), decision.Action().Permission())
}

type allowingPostPermissionChecker struct {
	permissions []string
	decisions   []policyv1.AuthorizationDecision
	allowed     bool
}

func (c *allowingPostPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.decisions = append(c.decisions, decision)
	c.permissions = append(c.permissions, decision.Action().Permission())
	return c.allowed, nil
}

func TestPostActionAuthorizationChecksExactlyOneSelectedPermission(t *testing.T) {
	principal := &auth.UserInfo{
		IdentityID:    "11111111-1111-4111-8111-111111111111",
		MemberID:      "22222222-2222-4222-8222-222222222222",
		SessionID:     "post-authorization-test-session",
		Authenticated: true,
	}
	postID := "33333333-3333-4333-8333-333333333333"
	tests := []struct {
		name     string
		status   model.PostStatus
		ordinary postAction
		want     postAction
	}{
		{name: "ordinary edit", status: model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()), ordinary: policyv1.Post.Edit, want: policyv1.Post.Edit},
		{name: "ordinary publish", status: model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()), ordinary: policyv1.Post.Publish, want: policyv1.Post.Publish},
		{name: "ordinary remove author", status: model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()), ordinary: policyv1.Post.RemoveAuthor, want: policyv1.Post.RemoveAuthor},
		{name: "archived mutation", status: model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()), ordinary: policyv1.Post.ManageParticipants, want: policyv1.Post.EditArchived},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker := &allowingPostPermissionChecker{allowed: true}
			_, err := requirePostActionPermissionForPrincipal(context.Background(), checker, postID, principal, test.status, test.ordinary, false)
			require.NoError(t, err)
			want, err := test.want(postID)
			require.NoError(t, err)
			require.Equal(t, []string{want.Action().Permission()}, checker.permissions)
		})
	}
}

func TestArchivedPostViewChecksOnlyViewArchived(t *testing.T) {
	checker := &allowingPostPermissionChecker{allowed: true}
	principal := &auth.UserInfo{
		IdentityID:    "11111111-1111-4111-8111-111111111111",
		MemberID:      "22222222-2222-4222-8222-222222222222",
		SessionID:     "post-authorization-test-session",
		Authenticated: true,
	}
	_, err := requirePostViewPermissionForPrincipal(
		context.Background(), checker, "33333333-3333-4333-8333-333333333333", principal,
		model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()),
	)
	require.NoError(t, err)
	want, err := policyv1.Post.ViewArchived("33333333-3333-4333-8333-333333333333")
	require.NoError(t, err)
	require.Equal(t, []string{want.Action().Permission()}, checker.permissions)
}

func TestArchivedPostMutationDenialDoesNotFallbackToOrdinaryPermission(t *testing.T) {
	checker := &allowingPostPermissionChecker{}
	principal := &auth.UserInfo{
		IdentityID:    "11111111-1111-4111-8111-111111111111",
		MemberID:      "22222222-2222-4222-8222-222222222222",
		SessionID:     "post-authorization-test-session",
		Authenticated: true,
	}
	_, err := requirePostActionPermissionForPrincipal(
		context.Background(), checker, "33333333-3333-4333-8333-333333333333", principal,
		model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()),
		policyv1.Post.ManageShareLinks,
		false,
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	want, err := policyv1.Post.EditArchived("33333333-3333-4333-8333-333333333333")
	require.NoError(t, err)
	require.Equal(t, []string{want.Action().Permission()}, checker.permissions)
}
