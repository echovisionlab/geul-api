//go:build integration

package post_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestPostConfigurationRevisionContractIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminIdentityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminIdentityID, "Post configuration revision admin")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	memberID := testutil.PostIntegrationMemberID(adminIdentityID)
	ctx := withPostAuditedRequestContext(t, testutil.PostIntegrationContext(adminIdentityID))
	store := testutil.NewPostContentBlockStore(t)
	events := &postConfigurationEventCapture{}
	service := newAuditedPostConfigurationService(t, db, spiceDB, adminIdentityID, store, events)

	slug := "post-configuration-revision-" + testutil.PostIntegrationUUID()
	created, err := service.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Post configuration revision", Slug: &slug, Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	require.NotEmpty(t, created.Msg.ConfigurationRevision)
	initial := requirePostConfigurationState(t, db, created.Msg.Id)
	require.Equal(t, initial.ConfigurationRevision, created.Msg.ConfigurationRevision)

	initialAudits := postConfigurationAuditCount(t, db, created.Msg.Id)
	unchanged := false
	_, err = service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, CommentsEnabled: &unchanged,
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "reload")
	malformed := "not-a-uuid"
	_, err = service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: malformed, CommentsEnabled: &unchanged,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Equal(t, initial, requirePostConfigurationState(t, db, created.Msg.Id))
	require.Equal(t, initialAudits, postConfigurationAuditCount(t, db, created.Msg.Id))
	require.Zero(t, events.count())

	noOp, err := service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: created.Msg.ConfigurationRevision, CommentsEnabled: &unchanged,
	}))
	require.NoError(t, err)
	require.False(t, noOp.Msg.Changed)
	require.Equal(t, initial.ConfigurationRevision, noOp.Msg.ConfigurationRevision)
	require.Equal(t, initial, requirePostConfigurationState(t, db, created.Msg.Id))
	require.Equal(t, initialAudits, postConfigurationAuditCount(t, db, created.Msg.Id))
	require.Zero(t, events.count())

	commentsEnabled := true
	firstUpdate, err := service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: initial.ConfigurationRevision, CommentsEnabled: &commentsEnabled,
	}))
	require.NoError(t, err)
	require.True(t, firstUpdate.Msg.Changed)
	require.NotEqual(t, initial.ConfigurationRevision, firstUpdate.Msg.ConfigurationRevision)
	committed := requirePostConfigurationState(t, db, created.Msg.Id)
	require.Equal(t, firstUpdate.Msg.ConfigurationRevision, committed.ConfigurationRevision)
	require.True(t, committed.CommentsEnabled)
	require.Equal(t, initialAudits+1, postConfigurationAuditCount(t, db, created.Msg.Id))
	require.Zero(t, events.count(), "configuration changes do not publish document content events")

	secondClientSlug := slug + "-stale"
	_, err = service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: initial.ConfigurationRevision, Slug: &secondClientSlug,
	}))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "reload")
	require.Equal(t, committed, requirePostConfigurationState(t, db, created.Msg.Id))
	require.Equal(t, initialAudits+1, postConfigurationAuditCount(t, db, created.Msg.Id))
	require.Zero(t, events.count())

	firstRevision := committed.ConfigurationRevision
	commentsEnabled = false
	abaFirst, err := service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: firstRevision, CommentsEnabled: &commentsEnabled,
	}))
	require.NoError(t, err)
	require.NotEqual(t, firstRevision, abaFirst.Msg.ConfigurationRevision)
	commentsEnabled = true
	abaSecond, err := service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
		Id: created.Msg.Id, ExpectedConfigurationRevision: abaFirst.Msg.ConfigurationRevision, CommentsEnabled: &commentsEnabled,
	}))
	require.NoError(t, err)
	require.NotEqual(t, abaFirst.Msg.ConfigurationRevision, abaSecond.Msg.ConfigurationRevision)
	require.NotEqual(t, firstRevision, abaSecond.Msg.ConfigurationRevision,
		"returning settings to an earlier value must not reuse the earlier revision")

	got, err := service.GetPost(ctx, connect.NewRequest(&managev1.GetPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, abaSecond.Msg.ConfigurationRevision, got.Msg.ConfigurationRevision)
	listed, err := service.ListPostsAdmin(ctx, connect.NewRequest(&managev1.ListPostsAdminRequest{}))
	require.NoError(t, err)
	require.Equal(t, abaSecond.Msg.ConfigurationRevision, findPostConfigurationRevision(t, listed.Msg.Posts, created.Msg.Id))

	beforeLifecycle := abaSecond.Msg.ConfigurationRevision
	_, err = service.PublishPost(ctx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, beforeLifecycle, requirePostConfigurationRevision(t, db, created.Msg.Id))
	_, err = service.UnpublishPost(ctx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, beforeLifecycle, requirePostConfigurationRevision(t, db, created.Msg.Id))

	blockID := testutil.PostIntegrationUUID()
	internal := postintegration.NewInternalPostDomainService(t, db, "", spiceDB, store)
	bodyWrite, err := internal.ApplyPostBlockBatch(
		t.Context(),
		connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
			PostId: created.Msg.Id, Locale: "en",
			Batch: postParagraphBatch(
				created.Msg.Revision, memberID, "en", []string{blockID}, []string{"new source body"}, true,
			),
			AffectedLocaleValues: postParagraphContentTargets(blockID),
		}),
	)
	require.NoError(t, err)
	require.True(t, bodyWrite.Msg.Changed)
	require.Equal(t, beforeLifecycle, requirePostConfigurationRevision(t, db, created.Msg.Id))

	titleWrite, err := internal.UpdatePostLocaleMetadata(
		t.Context(),
		connect.NewRequest(&intrav1.UpdatePostLocaleMetadataRequest{
			PostId: created.Msg.Id, Locale: "en", ExpectedRevision: bodyWrite.Msg.DocumentRevision,
			ContributorMemberIds: []string{memberID},
			TitleChange: &intrav1.PostNullableStringChange{
				Value: &intrav1.PostNullableStringChange_SetValue{SetValue: "Updated source title"},
			},
		}),
	)
	require.NoError(t, err)
	require.True(t, titleWrite.Msg.Changed)
	require.Equal(t, beforeLifecycle, requirePostConfigurationRevision(t, db, created.Msg.Id))
	got, err = service.GetPost(ctx, connect.NewRequest(&managev1.GetPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, "Updated source title", got.Msg.Title)
	discovered, err := service.ListAIDocuments(ctx, postdomain.AIDocumentListInput{Query: "Updated source title"})
	require.NoError(t, err)
	require.Len(t, discovered.Items, 1)
	require.Equal(t, beforeLifecycle, discovered.Items[0].ConfigurationRevision)
}

func TestPostConfigurationRevisionSerializesConcurrentUpdatesIntegration(t *testing.T) {
	db, spiceDB, actorID, _, postID := seedPostAuthorityRaceFixtureWithSpiceDB(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(actorID, "en")),
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
	requirePostMutationStillWaiting(t, pending)
	requirePostMutationStillWaiting(t, pending)

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

func newAuditedPostConfigurationService(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	adminIdentityID string,
	store *contentblock.Store,
	events *postConfigurationEventCapture,
) *postdomain.PostService {
	t.Helper()
	return postdomain.NewAuditedPostService(
		db,
		"",
		postintegration.NewPostOGRefresher(db, ""),
		spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminIdentityID, "en")),
		postAuditFiles{},
		events,
		postruntime.ShareLinks{},
		postruntime.ContentBlockMedia{},
		postadapter.NewMemberSummaries(db, ""),
		postruntime.VersionRestore{},
		apitelemetry.NewDurableWriter(db),
		postdomain.WithPostContentBlockStore(store),
	)
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

func requirePostConfigurationState(t *testing.T, db *gorm.DB, postID string) postConfigurationState {
	t.Helper()
	var state postConfigurationState
	require.NoError(t, db.Table("post").
		Select("slug, comments_enabled, configuration_revision, updated_at").
		Where("id = ?", postID).
		Take(&state).Error)
	return state
}

type postConfigurationState struct {
	Slug                  *string
	CommentsEnabled       bool
	ConfigurationRevision string
	UpdatedAt             time.Time
}

func postConfigurationAuditCount(t *testing.T, db *gorm.DB, postID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("domain_audit").Where("target_type = ? AND target_id = ?", "post", postID).Count(&count).Error)
	return count
}

func findPostConfigurationRevision(t *testing.T, posts []*managev1.PostWithStats, postID string) string {
	t.Helper()
	for _, item := range posts {
		if item.GetPost().GetId() == postID {
			return item.GetPost().GetConfigurationRevision()
		}
	}
	require.FailNow(t, "Post not found in admin list", "post id: %s", postID)
	return ""
}

type postConfigurationEventCapture struct {
	mu            sync.Mutex
	notifications int
}

func (capture *postConfigurationEventCapture) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (capture *postConfigurationEventCapture) NotifyProtobuf(context.Context, string, proto.Message) error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.notifications++
	return nil
}

func (capture *postConfigurationEventCapture) count() int {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.notifications
}
