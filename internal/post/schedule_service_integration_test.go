//go:build integration

package post_test

import (
	"testing"
	"time"

	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestProcessDueScheduledPostsDoesNotPublishTranslationWakeups(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	now := time.Now().UTC().Truncate(time.Minute)
	dueIDs := []string{testutil.IntegrationUUID(), testutil.IntegrationUUID()}
	futureID := testutil.IntegrationUUID()
	documentIDs := []string{testutil.IntegrationUUID(), testutil.IntegrationUUID(), testutil.IntegrationUUID()}

	for index, postID := range append(dueIDs, futureID) {
		documentID := documentIDs[index]
		scheduledAt := now.Add(time.Hour)
		if index < len(dueIDs) {
			scheduledAt = now.Add(-time.Minute)
		}
		require.NoError(t, db.Exec(
			`INSERT INTO content_document (id, profile) VALUES (?, 'post')`, documentID,
		).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO post (
			 id, status, scheduled_at, scheduled_time_zone,
			 created_at, updated_at, content_document_id
			) VALUES (?, ?, ?, 'Asia/Seoul', NOW(), NOW(), ?)`,
			postID, managev1.PostStatus_POST_STATUS_SCHEDULED.String(), scheduledAt, documentID,
		).Error)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DELETE FROM post WHERE id IN ?", append(dueIDs, futureID)).Error)
		require.NoError(t, db.Exec("DELETE FROM content_document WHERE id IN ?", documentIDs).Error)
	})

	published, err := postdomain.ProcessDueScheduledPosts(t.Context(), db, 100)
	require.NoError(t, err)
	require.ElementsMatch(t, dueIDs, published)

	published, err = postdomain.ProcessDueScheduledPosts(t.Context(), db, 100)
	require.NoError(t, err)
	require.Empty(t, published)

	var futureStatus string
	require.NoError(t, db.Table("post").Select("status").Where("id = ?", futureID).Scan(&futureStatus).Error)
	require.Equal(t, managev1.PostStatus_POST_STATUS_SCHEDULED.String(), futureStatus)
}
