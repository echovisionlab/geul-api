package ogadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
)

func TestRequireAuthenticatedRejectsUnavailablePrincipals(t *testing.T) {
	authorization := Authorization{}
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(authorization.RequireAuthenticated(context.Background())))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(authorization.RequireAuthenticated(auth.WithUser(
		context.Background(), &auth.UserInfo{Authenticated: false},
	))))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(authorization.RequireAuthenticated(auth.WithUser(
		context.Background(), &auth.UserInfo{Authenticated: true, Banned: true},
	))))
	require.NoError(t, authorization.RequireAuthenticated(auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), Authenticated: true,
	})))
}

func TestEntityCanKeepsStaticTargetsAdminOnly(t *testing.T) {
	entityID := uuid.NewString()
	for _, entityType := range []string{"site", "privacy", "terms", "unknown"} {
		_, err := entityCan(entityType, entityID, false)
		require.Error(t, err, entityType)
	}
	for _, entityType := range []string{"post", "page", "work", "artist", "release", "label", "series", "form", "campaign", "email_template"} {
		for _, requireEdit := range []bool{false, true} {
			can, err := entityCan(entityType, entityID, requireEdit)
			require.NoError(t, err, entityType)
			require.True(t, can.Valid(), entityType)
		}
	}
}

func TestStaticTargetsRemainAdminOnly(t *testing.T) {
	author, admin := uuid.NewString(), uuid.NewString()
	checker := (&authztest.Checker{}).For(admin).Allow(policyv1.Platform.IsAdmin())
	authorization := NewAuthorization(checker)

	authorCtx := auth.WithUser(context.Background(), principal(author))
	for _, entityType := range []string{"site", "privacy", "terms"} {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(
			authorization.AuthorizeEntity(authorCtx, entityType, uuid.NewString(), true),
		))
	}
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(authorization.RequireAdmin(authorCtx)))
	adminCtx := auth.WithUser(context.Background(), principal(admin))
	require.NoError(t, authorization.AuthorizeEntity(adminCtx, "privacy", uuid.NewString(), true))
	require.NoError(t, authorization.RequireAdmin(adminCtx))
}

func TestAuthorizeEntityAsksForTheEntityActionAfterAdmin(t *testing.T) {
	editor, postID := uuid.NewString(), uuid.NewString()
	checker := (&authztest.Checker{}).For(editor).Allow(policyv1.Post.Edit(postID))
	authorization := NewAuthorization(checker)
	ctx := auth.WithUser(context.Background(), principal(editor))

	require.NoError(t, authorization.AuthorizeEntity(ctx, "post", postID, true))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(authorization.AuthorizeEntity(ctx, "post", postID, false)))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(NewAuthorization((&authztest.Checker{}).Fail(errors.New("down"))).
		AuthorizeEntity(ctx, "post", postID, true)))
}

func principal(identityID string) *auth.UserInfo {
	return &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	}
}
