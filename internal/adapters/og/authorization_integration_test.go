//go:build integration

package ogadapter

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestEntityAuthorizationIsScopedToManagedResourceIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.User().ID())
	managedID, otherID := uuid.NewString(), uuid.NewString()
	actor, err := policyv1.NewAccountIdentityActor(manager.IdentityID)
	require.NoError(t, err)
	touchManager, err := policyv1.Artist.TouchManager(managedID, actor)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), touchManager)
	require.NoError(t, err)

	authorization := NewAuthorization(stack.SpiceDBClient)
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	require.NoError(t, authorization.AuthorizeEntity(ctx, "artist", managedID, true))
	require.NoError(t, authorization.AuthorizeEntity(ctx, "artist", managedID, false))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(authorization.AuthorizeEntity(ctx, "artist", otherID, true)))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(authorization.AuthorizeEntity(ctx, "artist", otherID, false)))
}

func TestStaticTargetsRemainAdminOnlyIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	authorization := NewAuthorization(stack.SpiceDBClient)

	authorCtx := auth.WithUser(context.Background(), author.AuthUserInfo())
	for _, entityType := range []string{"site", "privacy", "terms"} {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(
			authorization.AuthorizeEntity(authorCtx, entityType, uuid.NewString(), true),
		))
	}
	adminCtx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	require.NoError(t, authorization.AuthorizeEntity(adminCtx, "privacy", uuid.NewString(), true))
	require.NoError(t, authorization.RequireAdmin(adminCtx))
}
