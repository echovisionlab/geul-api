package public

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestManifestVisibilityUsesOnlyAuthenticatedRequestContext(t *testing.T) {
	service := &ManifestService{}

	user, err := service.getUserContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, userContext{}, user)

	forged := connect.NewRequest(&openv1.GetRequest{})
	forged.Header().Set("X-User-Id", "user-id")
	forged.Header().Set("X-User-Role", "author")
	user, err = service.getUserContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, userContext{}, user)

	require.True(t, service.isItemVisible(&model.MenuItem{}, userContext{}))
	require.True(t, service.isItemVisible(&model.MenuItem{Visibility: &model.MenuVisibility{Mode: ""}}, userContext{}))
	require.True(t, service.isItemVisible(&model.MenuItem{Visibility: &model.MenuVisibility{Mode: "all"}}, userContext{}))
	require.True(t, service.isItemVisible(&model.MenuItem{Visibility: &model.MenuVisibility{Mode: "unexpected"}}, userContext{}))
	require.False(t, service.isItemVisible(&model.MenuItem{Visibility: &model.MenuVisibility{
		Mode: "roles", Roles: []string{"admin"},
	}}, userContext{isAuthenticated: true, rolePermissions: map[string]bool{"author": true}}))
	require.False(t, service.isItemVisible(&model.MenuItem{Visibility: &model.MenuVisibility{
		Mode: "roles", Roles: []string{"author"},
	}}, userContext{rolePermissions: map[string]bool{"author": true}}))
}
