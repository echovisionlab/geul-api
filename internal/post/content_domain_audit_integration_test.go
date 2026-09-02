//go:build integration

package post_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func TestPostDomainAuditClosesLifecycleVersionAndDeleteBoundariesIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminIdentityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminIdentityID, "Post audit admin")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	memberID := testutil.PostIntegrationMemberID(adminIdentityID)
	memberCtx := withPostAuditedRequestContext(t, testutil.PostIntegrationContext(adminIdentityID))
	writer := apitelemetry.NewDurableWriter(db)
	files := postAuditFiles{}
	contentBlocks, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	postService := postdomain.NewAuditedPostService(
		db,
		"",
		postintegration.NewPostOGRefresher(db, ""),
		spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminIdentityID, "en")),
		files,
		postAuditAsyncPublisher{},
		postruntime.ShareLinks{},
		postruntime.ContentBlockMedia{},
		postadapter.NewMemberSummaries(db, ""),
		postruntime.VersionRestore{},
		writer,
		postdomain.WithPostContentBlockStore(contentBlocks),
	)
	internalService := postdomain.NewInternalPostService(
		db,
		spiceDB,
		postAuditAsyncPublisher{},
		"",
		postintegration.NewPostOGRefresher(db, ""),
		postruntime.ContentBlockMedia{},
		postdomain.WithInternalPostDomainAuditWriter(writer),
		postdomain.WithInternalPostContentBlockStore(contentBlocks),
	)

	created, err := postService.CreatePost(memberCtx, connect.NewRequest(&managev1.CreatePostRequest{
		Title:    "Audited post",
		Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	_, err = postService.PublishPost(memberCtx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = postService.UnpublishPost(memberCtx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	scheduledAt := time.Now().UTC().Truncate(time.Minute).Add(time.Hour)
	_, err = postService.SchedulePost(memberCtx, connect.NewRequest(&managev1.SchedulePostRequest{
		Id: created.Msg.Id, ScheduledAt: timestamppb.New(scheduledAt), ScheduledTimeZone: "UTC",
	}))
	require.NoError(t, err)
	_, err = postService.CancelPostSchedule(memberCtx, connect.NewRequest(&managev1.CancelPostScheduleRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = postService.PublishPost(memberCtx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = postService.ArchivePost(memberCtx, connect.NewRequest(&managev1.ArchivePostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = postService.RepublishPost(memberCtx, connect.NewRequest(&managev1.RepublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = postService.UnpublishPost(memberCtx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	contributorID := seedPostAuditContributor(t, db, spiceDB, created.Msg.Id)
	checkpoint, err := internalService.CreatePostVersionCheckpoint(
		postAuditedSystemRequestContext(t),
		connect.NewRequest(&intrav1.CreatePostVersionCheckpointRequest{
			PostId:               created.Msg.Id,
			ExpectedRevision:     created.Msg.Revision,
			ContributorMemberIds: []string{contributorID},
			Locale:               "en",
		}),
	)
	require.NoError(t, err)
	require.True(t, checkpoint.Msg.Created)
	_, err = postService.DeletePost(memberCtx, connect.NewRequest(&managev1.DeletePostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)

	requirePostContentAuditRecords(t, db, created.Msg.Id, memberID, contributorID, []postContentAuditExpectation{
		{action: sharedtelemetry.AuditPostCreated},
		postLifecycleAuditExpectation("status", sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStatePublished),
		postLifecycleAuditExpectation("status", sharedtelemetry.AuditStatePublished, sharedtelemetry.AuditStateDraft),
		postLifecycleAuditExpectation("schedule", sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStateScheduled),
		postLifecycleAuditExpectation("schedule", sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft),
		postLifecycleAuditExpectation("status", sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStatePublished),
		postLifecycleAuditExpectation("status", sharedtelemetry.AuditStatePublished, sharedtelemetry.AuditStateArchived),
		postLifecycleAuditExpectation("status", sharedtelemetry.AuditStateArchived, sharedtelemetry.AuditStatePublished),
		postLifecycleAuditExpectation("status", sharedtelemetry.AuditStatePublished, sharedtelemetry.AuditStateDraft),
		{action: sharedtelemetry.AuditPostUpdated, changedField: "version", system: true},
		{action: sharedtelemetry.AuditPostDeleted},
	})
}

type postAuditFiles struct{}

func (postAuditFiles) DeleteFileByID(context.Context, string) error { return nil }
func (postAuditFiles) ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error) {
	return nil, nil
}
func (postAuditFiles) ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error) {
	return map[string]*commonv1.MediaDelivery{}, nil
}

type postContentAuditExpectation struct {
	action       sharedtelemetry.AuditAction
	changedField string
	previous     sharedtelemetry.AuditState
	next         sharedtelemetry.AuditState
	system       bool
}

func postLifecycleAuditExpectation(field string, previous, next sharedtelemetry.AuditState) postContentAuditExpectation {
	return postContentAuditExpectation{action: sharedtelemetry.AuditPostUpdated, changedField: field, previous: previous, next: next}
}

type postAuditAsyncPublisher struct{}

func (postAuditAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (postAuditAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func withPostAuditedRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.99")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), user)
}

func postAuditedSystemRequestContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: "geul-collab"},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}

func requirePostContentAuditRecords(
	t *testing.T,
	db *gorm.DB,
	targetID string,
	memberID string,
	contributorID string,
	want []postContentAuditExpectation,
) {
	t.Helper()
	var records []struct {
		Action               string
		ActorKind            string
		ActorMemberID        *string
		ActorService         *string
		ChangedFields        pq.StringArray `gorm:"type:text[]"`
		PreviousState        *string
		NewState             *string
		VersionID            *string
		ContributorMemberIDs pq.StringArray `gorm:"type:uuid[]"`
	}
	require.NoError(t, db.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id,
		       actor_service,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'previous_state' AS previous_state,
		       attributes->>'new_state' AS new_state,
		       attributes->>'version_id' AS version_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'contributor_member_ids')) AS contributor_member_ids
		FROM public.domain_audit
		WHERE target_type = 'post' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, targetID).Scan(&records).Error)
	require.Len(t, records, len(want))
	for index, expected := range want {
		require.Equal(t, string(expected.action), records[index].Action)
		if expected.system {
			require.Equal(t, pq.StringArray{"version"}, records[index].ChangedFields)
			require.Equal(t, string(sharedtelemetry.ActorKindSystem), records[index].ActorKind)
			require.Equal(t, "geul-collab", postAuditStringPointer(t, records[index].ActorService))
			require.Nil(t, records[index].ActorMemberID)
			require.NotEmpty(t, postAuditStringPointer(t, records[index].VersionID))
			require.Equal(t, pq.StringArray{contributorID}, records[index].ContributorMemberIDs)
			continue
		}
		if expected.changedField != "" {
			require.Equal(t, pq.StringArray{expected.changedField}, records[index].ChangedFields)
			require.Equal(t, string(sharedtelemetry.ActorKindMember), records[index].ActorKind)
			require.Equal(t, memberID, postAuditStringPointer(t, records[index].ActorMemberID))
			require.Nil(t, records[index].ActorService)
			require.Equal(t, string(expected.previous), postAuditStringPointer(t, records[index].PreviousState))
			require.Equal(t, string(expected.next), postAuditStringPointer(t, records[index].NewState))
			continue
		}
		require.Equal(t, string(sharedtelemetry.ActorKindMember), records[index].ActorKind)
		require.Equal(t, memberID, postAuditStringPointer(t, records[index].ActorMemberID))
		require.Nil(t, records[index].ActorService)
		require.Empty(t, records[index].ChangedFields)
		require.Nil(t, records[index].VersionID)
		require.Empty(t, records[index].ContributorMemberIDs)
	}
}

func seedPostAuditContributor(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	postID string,
) string {
	t.Helper()
	identityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Post audit contributor")
	memberID := testutil.PostIntegrationMemberID(identityID)
	require.NoError(t, db.Exec(`
		INSERT INTO post_collaborator (post_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, NOW())
	`, postID, memberID).Error)
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	mutation, err := policyv1.Post.TouchCollaborator(postID, actor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
	return memberID
}

func postAuditStringPointer(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}
