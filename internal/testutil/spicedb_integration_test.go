//go:build integration

package testutil

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type scriptedIntegrationSpiceDBRelationshipDeleter struct {
	responses []*v1.DeleteRelationshipsResponse
	repeat    *v1.DeleteRelationshipsResponse
	err       error
	calls     int
	requests  []*v1.DeleteRelationshipsRequest
}

func (deleter *scriptedIntegrationSpiceDBRelationshipDeleter) DeleteRelationships(
	_ context.Context,
	request *v1.DeleteRelationshipsRequest,
	_ ...grpc.CallOption,
) (*v1.DeleteRelationshipsResponse, error) {
	deleter.calls++
	deleter.requests = append(deleter.requests, request)
	if deleter.err != nil {
		return nil, deleter.err
	}
	if deleter.calls <= len(deleter.responses) {
		return deleter.responses[deleter.calls-1], nil
	}
	return deleter.repeat, nil
}

func TestDeleteIntegrationSpiceDBDefinitionRelationshipsExhaustsPartialDeletion(t *testing.T) {
	deleter := &scriptedIntegrationSpiceDBRelationshipDeleter{responses: []*v1.DeleteRelationshipsResponse{
		{
			DeletionProgress:          v1.DeleteRelationshipsResponse_DELETION_PROGRESS_PARTIAL,
			RelationshipsDeletedCount: integrationSpiceDBResetDeleteBatchSize,
		},
		{DeletionProgress: v1.DeleteRelationshipsResponse_DELETION_PROGRESS_COMPLETE},
	}}

	require.NoError(t, deleteIntegrationSpiceDBDefinitionRelationships(t.Context(), deleter, "post"))
	require.Equal(t, 2, deleter.calls)
	for _, request := range deleter.requests {
		require.Equal(t, "post", request.GetRelationshipFilter().GetResourceType())
		require.EqualValues(t, integrationSpiceDBResetDeleteBatchSize, request.GetOptionalLimit())
		require.True(t, request.GetOptionalAllowPartialDeletions())
	}
}

func TestDeleteIntegrationSpiceDBDefinitionRelationshipsFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		deleter  *scriptedIntegrationSpiceDBRelationshipDeleter
		contains string
	}{
		{
			name:     "rpc error",
			deleter:  &scriptedIntegrationSpiceDBRelationshipDeleter{err: errors.New("delete unavailable")},
			contains: "delete unavailable",
		},
		{
			name:     "empty response",
			deleter:  &scriptedIntegrationSpiceDBRelationshipDeleter{responses: []*v1.DeleteRelationshipsResponse{nil}},
			contains: "empty response",
		},
		{
			name: "unspecified progress",
			deleter: &scriptedIntegrationSpiceDBRelationshipDeleter{responses: []*v1.DeleteRelationshipsResponse{{
				DeletionProgress: v1.DeleteRelationshipsResponse_DELETION_PROGRESS_UNSPECIFIED,
			}}},
			contains: "UNSPECIFIED",
		},
		{
			name: "partial without progress",
			deleter: &scriptedIntegrationSpiceDBRelationshipDeleter{responses: []*v1.DeleteRelationshipsResponse{{
				DeletionProgress: v1.DeleteRelationshipsResponse_DELETION_PROGRESS_PARTIAL,
			}}},
			contains: "made no progress",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := deleteIntegrationSpiceDBDefinitionRelationships(t.Context(), test.deleter, "post")
			require.ErrorContains(t, err, test.contains)
			require.Equal(t, 1, test.deleter.calls)
		})
	}
}

func TestDeleteIntegrationSpiceDBDefinitionRelationshipsStopsAtBound(t *testing.T) {
	deleter := &scriptedIntegrationSpiceDBRelationshipDeleter{repeat: &v1.DeleteRelationshipsResponse{
		DeletionProgress:          v1.DeleteRelationshipsResponse_DELETION_PROGRESS_PARTIAL,
		RelationshipsDeletedCount: 1,
	}}

	err := deleteIntegrationSpiceDBDefinitionRelationships(t.Context(), deleter, "post")
	require.ErrorContains(t, err, "did not complete")
	require.Equal(t, integrationSpiceDBResetMaxDeleteBatchCount, deleter.calls)
}

func TestBackendIntegrationSpiceDBLoadsGeneratedSchemaAndRoleGraph(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	stack, err := StartBackendIntegrationStack(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	require.NotEmpty(t, stack.SpiceDBEndpoint)
	require.Equal(t, spiceDBIntegrationToken, stack.SpiceDBToken)

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
