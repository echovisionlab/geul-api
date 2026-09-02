//go:build integration

package post_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestDeletePostRejectsOversizedAuthorizationSnapshotBeforeMutationIntegration(t *testing.T) {
	const relationshipMutationLimit = 1000
	db := testutil.NewPostIntegrationDB(t)
	adminID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Post delete batch boundary admin")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(adminID)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en")),
		testutil.NewPostContentBlockStore(t),
	)

	created, err := service.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title:    "Oversized authorization snapshot",
		Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)

	snapshotPlan, err := policyv1.Post.Snapshot(created.Msg.Id)
	require.NoError(t, err)
	additional := make([]policyv1.RelationshipMutation, 0, relationshipMutationLimit-1)
	for range relationshipMutationLimit - 1 {
		actor, actorErr := policyv1.NewAccountIdentityActor(testutil.PostIntegrationUUID())
		require.NoError(t, actorErr)
		mutation, mutationErr := policyv1.Post.TouchCollaborator(created.Msg.Id, actor)
		require.NoError(t, mutationErr)
		additional = append(additional, mutation)
	}
	_, err = spiceDB.ApplyRelationships(ctx, additional...)
	require.NoError(t, err)

	before, _, err := spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
	require.NoError(t, err)
	require.Len(t, before, relationshipMutationLimit+1)

	response, err := service.DeletePost(ctx, connect.NewRequest(&managev1.DeletePostRequest{Id: created.Msg.Id}))
	require.Nil(t, response)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "remove participant relationships or reparent dependent resources first")

	var persisted model.Post
	require.NoError(t, db.First(&persisted, "id = ?", created.Msg.Id).Error)
	require.Equal(t, created.Msg.Id, persisted.ID)
	testutil.RequirePostAuthorization(t, spiceDB, created.Msg.Id, adminID, policyv1.Post.Edit, true)

	after, _, err := spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
	require.NoError(t, err)
	require.Len(t, after, len(before))
}
