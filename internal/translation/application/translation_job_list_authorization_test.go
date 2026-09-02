//go:build integration

package application

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestListTranslationJobsAllowsExactManagedEntityScope(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	entityID := uuid.NewString()
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	active := seedTranslationRetryTestJob(t, db, translationJobStatusQueued, "operation", entityID)
	seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "source_update", entityID)
	seedTranslationRetryTestJob(t, db, translationJobStatusQueued, "operation", uuid.NewString())

	actor, err := policyv1.NewAccountIdentityActor(user.IdentityID)
	require.NoError(t, err)
	touchCollaborator, err := policyv1.Post.TouchCollaborator(entityID, actor)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), touchCollaborator)
	require.NoError(t, err)
	svc := newTranslationServiceForOGTest(db, &stubTranslationServicePublisher{}, "", stack.SpiceDBClient)
	ctx := auth.WithUser(context.Background(), user.AuthUserInfo())

	response, err := svc.ListTranslationJobs(ctx, connect.NewRequest(&managev1.ListTranslationJobsRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 2},
		Filters: []*commonv1.FilterSpec{
			{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "post"},
			{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
			{
				Field:  "status",
				Op:     commonv1.FilterOp_FILTER_OP_IN,
				Values: []string{translationJobStatusQueued, translationJobStatusRunning},
			},
		},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Jobs, 2)
	jobIDs := make([]string, 0, len(response.Msg.Jobs))
	for _, job := range response.Msg.Jobs {
		jobIDs = append(jobIDs, job.Id)
	}
	require.Contains(t, jobIDs, active.ID)
	require.Equal(t, int32(2), response.Msg.Pagination.Total)
}

func TestListTranslationJobsRejectsUnscopedOrUnmanagedNonAdmin(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	svc := newTranslationServiceForOGTest(db, &stubTranslationServicePublisher{}, "", stack.SpiceDBClient)
	ctx := auth.WithUser(context.Background(), user.AuthUserInfo())

	_, err := svc.ListTranslationJobs(ctx, connect.NewRequest(&managev1.ListTranslationJobsRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = svc.ListTranslationJobs(ctx, connect.NewRequest(&managev1.ListTranslationJobsRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "post"},
			{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: uuid.NewString()},
		},
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestListTranslationJobsRejectsManagedWorkScopeForNonAdmin(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	entityID := uuid.NewString()
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	svc := newTranslationServiceForOGTest(db, &stubTranslationServicePublisher{}, "", stack.SpiceDBClient)
	ctx := auth.WithUser(context.Background(), user.AuthUserInfo())

	_, err := svc.ListTranslationJobs(ctx, connect.NewRequest(&managev1.ListTranslationJobsRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "work"},
			{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
		},
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestListTranslationJobsRejectsInvalidNonAdminScopeShapes(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	entityID := uuid.NewString()
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	svc := newTranslationServiceForOGTest(db, &stubTranslationServicePublisher{}, "", stack.SpiceDBClient)
	authenticatedCtx := auth.WithUser(context.Background(), user.AuthUserInfo())

	tests := []struct {
		name    string
		ctx     context.Context
		filters []*commonv1.FilterSpec
		code    connect.Code
	}{
		{
			name: "unauthenticated",
			ctx:  context.Background(),
			filters: []*commonv1.FilterSpec{
				{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "post"},
				{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
			},
			code: connect.CodeUnauthenticated,
		},
		{
			name: "duplicate entity type",
			ctx:  authenticatedCtx,
			filters: []*commonv1.FilterSpec{
				{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "post"},
				{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "post"},
				{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
			},
			code: connect.CodePermissionDenied,
		},
		{
			name: "entity type in filter",
			ctx:  authenticatedCtx,
			filters: []*commonv1.FilterSpec{
				{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_IN, Values: []string{"post"}},
				{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
			},
			code: connect.CodePermissionDenied,
		},
		{
			name: "entity id in filter",
			ctx:  authenticatedCtx,
			filters: []*commonv1.FilterSpec{
				{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "post"},
				{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_IN, Values: []string{entityID}},
			},
			code: connect.CodePermissionDenied,
		},
		{
			name: "unsupported entity type",
			ctx:  authenticatedCtx,
			filters: []*commonv1.FilterSpec{
				{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "unknown"},
				{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
			},
			code: connect.CodeInvalidArgument,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.ListTranslationJobs(test.ctx, connect.NewRequest(&managev1.ListTranslationJobsRequest{
				Filters: test.filters,
			}))
			require.Equal(t, test.code, connect.CodeOf(err))
		})
	}
}
