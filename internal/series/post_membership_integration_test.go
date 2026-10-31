//go:build integration

package series

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A Post's Series and position are Post data: every change advances the
// Post's updated_at, which the sitemap lastmod and updated listings read.
// Series used to write them without it.
func TestSeriesMembershipAdvancesPostUpdatedAtIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	seedSeriesActor(t, db, adminID, "Series membership admin")
	stack := testutil.SetupOryStack(t)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	service := auditedSeriesIntegrationService(t, db, adminID, stack.SpiceDBClient, apitelemetry.NewDurableWriter(db))
	ctx := seriesAuditContext(t, adminID)
	created, err := service.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{Title: "Series membership"}))
	require.NoError(t, err)
	seriesID := created.Msg.Id

	first := seedSeriesPost(t, db, testutil.PostIntegrationMemberID(adminID))
	second := seedSeriesPost(t, db, testutil.PostIntegrationMemberID(adminID))
	third := seedSeriesPost(t, db, testutil.PostIntegrationMemberID(adminID))
	for _, postID := range []string{first, second, third} {
		grantSeriesPostAuthorRelation(t, service, postID, adminID)
	}
	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	backdate := func(postIDs ...string) {
		require.NoError(t, db.Exec(`UPDATE post SET updated_at = ? WHERE id IN ?`, stale, postIDs).Error)
	}
	requireAdvanced := func(step string, advanced bool, postIDs ...string) {
		t.Helper()
		for _, postID := range postIDs {
			require.Equal(t, advanced, postUpdatedAt(t, db, postID).After(stale), "%s: post %s", step, postID)
		}
	}

	backdate(first, second, third)
	for _, postID := range []string{first, second, third} {
		_, err = service.AssignPostToSeries(ctx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{PostId: postID, SeriesId: seriesID}))
		require.NoError(t, err)
	}
	requireAdvanced("assign", true, first, second, third)

	backdate(first, second, third)
	_, err = service.ReorderSeriesPosts(ctx, connect.NewRequest(&managev1.ReorderSeriesPostsRequest{SeriesId: seriesID, PostIds: []string{second, first, third}}))
	require.NoError(t, err)
	requireAdvanced("reorder moves", true, first, second)
	requireAdvanced("reorder keeps", false, third)

	backdate(first, second, third)
	_, err = service.UnassignPostFromSeries(ctx, connect.NewRequest(&managev1.UnassignPostFromSeriesRequest{SeriesId: seriesID, PostId: second}))
	require.NoError(t, err)
	requireAdvanced("unassign", true, second, first)

	backdate(first, second, third)
	_, err = service.DeleteSeries(ctx, connect.NewRequest(&managev1.DeleteSeriesRequest{Id: seriesID}))
	require.NoError(t, err)
	requireAdvanced("delete series", true, first, third)
	requireAdvanced("delete series leaves", false, second)
}

func postUpdatedAt(t *testing.T, db *gorm.DB, postID string) time.Time {
	t.Helper()
	var updatedAt time.Time
	require.NoError(t, db.Raw(`SELECT updated_at FROM post WHERE id = ?`, postID).Scan(&updatedAt).Error)
	return updatedAt
}
