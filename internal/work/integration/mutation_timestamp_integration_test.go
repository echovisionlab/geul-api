//go:build integration

package integration

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Mutation responses must carry the instants PostgreSQL stores, so a client
// comparing them with a later read sees no change.
func TestWorkMutationResponsesMatchLaterReadsIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Work timestamp Admin")
	ctx := workIntegrationAdminCtx(adminID)
	workService := newWorkIntegrationService(t, db, adminID, referenceNoopFileDeleter{})
	isPresent := true

	created, err := workService.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Work timestamps " + testutil.IntegrationUUID(),
		Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
		Year:      2026,
		Month:     10,
		IsPresent: &isPresent,
	}))
	require.NoError(t, err)
	get := func() *managev1.Work {
		t.Helper()
		fetched, err := workService.GetWork(ctx, connect.NewRequest(&managev1.GetWorkRequest{Id: created.Msg.Id}))
		require.NoError(t, err)
		return fetched.Msg
	}

	slug := "work-timestamps-" + testutil.IntegrationUUID()
	updated, err := workService.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id: created.Msg.Id, Slug: &slug,
	}))
	require.NoError(t, err)
	require.True(t, get().GetUpdatedAt().AsTime().Equal(updated.Msg.GetUpdatedAt().AsTime()))

	published, err := workService.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	afterPublish := get()
	require.True(t, afterPublish.GetUpdatedAt().AsTime().Equal(published.Msg.GetUpdatedAt().AsTime()))
	require.True(t, afterPublish.GetPublishedAt().AsTime().Equal(published.Msg.GetPublishedAt().AsTime()))

	unpublished, err := workService.UnpublishWork(ctx, connect.NewRequest(&managev1.UnpublishWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.True(t, get().GetUpdatedAt().AsTime().Equal(unpublished.Msg.GetUpdatedAt().AsTime()))
}
