//go:build integration

package release_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Mutation responses must carry the instants PostgreSQL stores, so a client
// comparing them with a later read sees no change.
func TestReleaseMutationResponsesMatchLaterReadsIntegration(t *testing.T) {
	t.Parallel()
	fixture := newReleaseTrackAuthorityRaceFixture(t)
	get := func() *managev1.Release {
		t.Helper()
		fetched, err := fixture.releases.GetRelease(fixture.ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: fixture.release}))
		require.NoError(t, err)
		return fetched.Msg
	}

	catalog := "timestamp-catalog"
	updated, err := fixture.releases.UpdateRelease(fixture.ctx, connect.NewRequest(&managev1.UpdateReleaseRequest{
		Id: fixture.release, CatalogNumber: &catalog,
	}))
	require.NoError(t, err)
	require.True(t, get().GetUpdatedAt().AsTime().Equal(updated.Msg.GetUpdatedAt().AsTime()))

	published, err := fixture.releases.PublishRelease(fixture.ctx, connect.NewRequest(&managev1.PublishReleaseRequest{Id: fixture.release}))
	require.NoError(t, err)
	afterPublish := get()
	require.True(t, afterPublish.GetUpdatedAt().AsTime().Equal(published.Msg.GetUpdatedAt().AsTime()))
	require.True(t, afterPublish.GetPublishedAt().AsTime().Equal(published.Msg.GetPublishedAt().AsTime()))

	unpublished, err := fixture.releases.UnpublishRelease(fixture.ctx, connect.NewRequest(&managev1.UnpublishReleaseRequest{Id: fixture.release}))
	require.NoError(t, err)
	require.True(t, get().GetUpdatedAt().AsTime().Equal(unpublished.Msg.GetUpdatedAt().AsTime()))
}
