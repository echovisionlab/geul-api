package ogadapter

import (
	"context"
	"testing"

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
	for _, entityType := range []string{"post", "page", "work", "series", "form", "campaign", "email_template"} {
		for _, requireEdit := range []bool{false, true} {
			can, err := entityCan(entityType, entityID, requireEdit)
			require.NoError(t, err, entityType)
			require.True(t, can.Valid(), entityType)
		}
	}
}
