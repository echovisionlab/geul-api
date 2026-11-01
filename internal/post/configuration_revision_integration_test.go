//go:build integration

package post_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPostConfigurationRevisionSerializesConcurrentUpdatesIntegration(t *testing.T) {
	t.Parallel()
	db, spiceDB, actorID, _, postID := seedPostAuthorityRaceFixtureWithSpiceDB(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostContentBlockStore(t),
	)
	revision := requirePostConfigurationRevision(t, db, postID)

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	t.Cleanup(func() { _ = lockTx.Rollback().Error })
	require.NoError(t, lockTx.Exec("SELECT id FROM post WHERE id = ?::uuid FOR UPDATE", postID).Error)

	type updateResult struct {
		changed bool
		err     error
	}
	start := make(chan struct{}, 2)
	results := make(chan updateResult, 2)
	pending := make(chan error, 2)
	commentsEnabled := false
	newSlug := "post-concurrent-" + testutil.PostIntegrationUUID()
	go func() {
		start <- struct{}{}
		response, err := service.UpdatePost(postAuthorRaceContext(actorID), connect.NewRequest(&managev1.UpdatePostRequest{
			Id: postID, ExpectedConfigurationRevision: revision, CommentsEnabled: &commentsEnabled,
		}))
		result := updateResult{err: err}
		if response != nil {
			result.changed = response.Msg.Changed
		}
		results <- result
		pending <- err
	}()
	go func() {
		start <- struct{}{}
		response, err := service.UpdatePost(postAuthorRaceContext(actorID), connect.NewRequest(&managev1.UpdatePostRequest{
			Id: postID, ExpectedConfigurationRevision: revision, Slug: &newSlug,
		}))
		result := updateResult{err: err}
		if response != nil {
			result.changed = response.Msg.Changed
		}
		results <- result
		pending <- err
	}()
	<-start
	<-start
	requireMutationWaiting(t, db, 2, pending)

	require.NoError(t, lockTx.Commit().Error)
	first := <-results
	second := <-results
	successes := 0
	staleErrors := 0
	for _, result := range []updateResult{first, second} {
		if result.err == nil {
			require.True(t, result.changed)
			successes++
			continue
		}
		require.Equal(t, connect.CodeAborted, connect.CodeOf(result.err))
		staleErrors++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, staleErrors)
	require.NotEqual(t, revision, requirePostConfigurationRevision(t, db, postID))
}

func requirePostConfigurationRevision(t *testing.T, db *gorm.DB, postID string) string {
	t.Helper()
	var state struct {
		ConfigurationRevision string `gorm:"column:configuration_revision"`
	}
	require.NoError(t, db.Table("post").Select("configuration_revision").Where("id = ?", postID).Take(&state).Error)
	require.NotEmpty(t, state.ConfigurationRevision)
	return state.ConfigurationRevision
}
