//go:build integration

package runtimetest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	orytest "github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/stretchr/testify/require"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestBackendIntegrationSpiceDBLoadsGeneratedSchemaAndRoleGraph(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	stack, err := StartBackendIntegrationStack(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	require.NotEmpty(t, stack.SpiceDBEndpoint)
	require.NotEqual(t, orytest.SpiceDBIntegrationToken, stack.SpiceDBToken, "each stack gets its own datastore")

	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID("00000000-0000-4000-8000-000000000001"))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(ctx, subject, policyv1.Role.Admin())
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	for _, canFor := range []func() (policyv1.Can, error){
		policyv1.Platform.IsAdmin,
		policyv1.Platform.IsAuthor,
		policyv1.Platform.IsUser,
	} {
		can, canErr := canFor()
		require.NoError(t, canErr)
		allowed, checkErr := stack.SpiceDBClient.CheckActorCan(ctx, actor, can)
		require.NoError(t, checkErr)
		require.True(t, allowed, can.Action().Name())
	}
}

func TestBackendIntegrationOathkeeperLoadsIdentityRuntimeConfig(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	stack, err := StartBackendIntegrationStack(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, stack.OathkeeperAdminURL+"/health/ready", nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	require.Equal(t, http.StatusOK, response.StatusCode)
}

func TestBackendIntegrationStackStartsKratosOathkeeperAndSpiceDB(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	stack, err := StartBackendIntegrationStack(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })

	require.NotEmpty(t, stack.KratosAdminURL)
	require.NotEmpty(t, stack.KratosPublicURL)
	require.NotEmpty(t, stack.OathkeeperProxyURL)
	require.NotEmpty(t, stack.OathkeeperAdminURL)
	require.NotEmpty(t, stack.SpiceDBEndpoint)
	require.NotNil(t, stack.SpiceDBClient)
}
