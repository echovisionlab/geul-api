//go:build integration

package post_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type archivedUnpublishAuditWriter struct {
	writer *apitelemetry.DurableWriter
	fail   error
}

func (writer *archivedUnpublishAuditWriter) AppendDomainAuditInTransaction(
	ctx context.Context, tx *gorm.DB, record sharedtelemetry.AuditRecord,
) error {
	if writer.fail != nil {
		return writer.fail
	}
	return writer.writer.AppendDomainAuditInTransaction(ctx, tx, record)
}

func TestArchivedPostUnpublishPreservesAdminAuthorityAndAuditAtomicityIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminID, authorID := testutil.PostIntegrationUUID(), testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Archived unpublish Admin")
	testutil.SeedPostIntegrationIdentity(t, db, authorID, "Archived unpublish Author")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminID, policyv1.Role.Admin())
	testutil.GrantPostIntegrationRole(t, spiceDB, authorID, policyv1.Role.Author())
	adminCtx := withPostAuditedRequestContext(t, testutil.PostIntegrationContext(adminID))
	authorCtx := withPostAuditedRequestContext(t, testutil.PostIntegrationContext(authorID))
	writer := &archivedUnpublishAuditWriter{writer: apitelemetry.NewDurableWriter(db)}
	service := postdomain.NewAuditedPostService(
		db, "", postintegration.NewPostOGRefresher(db, ""), spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en")),
		postAuditFiles{}, postAuditAsyncPublisher{}, postruntime.ShareLinks{},
		postruntime.ContentBlockMedia{}, postadapter.NewMemberSummaries(db, ""),
		postruntime.VersionRestore{}, writer,
		postdomain.WithPostContentBlockStore(testutil.NewPostContentBlockStore(t)),
	)
	created, err := service.CreatePost(adminCtx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Archived Post withdrawn directly", Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	_, err = service.AddPostAuthor(adminCtx, connect.NewRequest(&managev1.AddPostAuthorRequest{
		PostId: created.Msg.Id, MemberId: testutil.PostIntegrationMemberID(authorID),
	}))
	require.NoError(t, err)
	_, err = service.PublishPost(adminCtx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	archived, err := service.ArchivePost(adminCtx, connect.NewRequest(&managev1.ArchivePostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Contains(t, archived.Msg.AllowedActions, managev1.PostAction_POST_ACTION_UNPUBLISH)
	authorPost, err := service.GetPost(authorCtx, connect.NewRequest(&managev1.GetPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.NotContains(t, authorPost.Msg.AllowedActions, managev1.PostAction_POST_ACTION_UNPUBLISH)
	var before model.Post
	require.NoError(t, db.First(&before, "id = ?", created.Msg.Id).Error)
	var auditCountBefore int64
	require.NoError(t, db.Table("domain_audit").Where("target_type = 'post' AND target_id = ?", created.Msg.Id).
		Count(&auditCountBefore).Error)

	_, err = service.UnpublishPost(authorCtx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var afterDenied model.Post
	require.NoError(t, db.First(&afterDenied, "id = ?", created.Msg.Id).Error)
	require.Equal(t, before.Status, afterDenied.Status)
	require.Equal(t, before.UpdatedAt, afterDenied.UpdatedAt)
	require.Equal(t, before.PublishedAt, afterDenied.PublishedAt)

	auditFailure := errors.New("archive withdrawal audit unavailable")
	writer.fail = auditFailure
	_, err = service.UnpublishPost(adminCtx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
	require.ErrorIs(t, err, auditFailure)
	writer.fail = nil
	var afterRollback model.Post
	require.NoError(t, db.First(&afterRollback, "id = ?", created.Msg.Id).Error)
	require.Equal(t, before.Status, afterRollback.Status)
	require.Equal(t, before.UpdatedAt, afterRollback.UpdatedAt)
	require.Equal(t, before.PublishedAt, afterRollback.PublishedAt)
	var auditCountAfter int64
	require.NoError(t, db.Table("domain_audit").Where("target_type = 'post' AND target_id = ?", created.Msg.Id).
		Count(&auditCountAfter).Error)
	require.Equal(t, auditCountBefore, auditCountAfter)

	withdrawn, err := service.UnpublishPost(adminCtx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.True(t, withdrawn.Msg.Changed)
	require.Equal(t, managev1.PostStatus_POST_STATUS_DRAFT, withdrawn.Msg.Status)
	require.True(t, withdrawn.Msg.UpdatedAt.AsTime().After(archived.Msg.UpdatedAt.AsTime()))
	require.Equal(t, archived.Msg.PublishedAt, withdrawn.Msg.PublishedAt)
	require.Nil(t, withdrawn.Msg.ScheduledAt)
	require.Nil(t, withdrawn.Msg.ScheduledTimeZone)
	require.Contains(t, withdrawn.Msg.AllowedActions, managev1.PostAction_POST_ACTION_PUBLISH_NOW)
	require.NotContains(t, withdrawn.Msg.AllowedActions, managev1.PostAction_POST_ACTION_UNPUBLISH)
	var after model.Post
	require.NoError(t, db.First(&after, "id = ?", created.Msg.Id).Error)
	require.Equal(t, model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()), after.Status)
	require.True(t, after.UpdatedAt.After(before.UpdatedAt))
	require.Equal(t, before.PublishedAt, after.PublishedAt)

	// A single archived->draft audit proves this call did not republish first.
	var lifecycle []struct{ PreviousState, NewState string }
	require.NoError(t, db.Raw(`
		SELECT attributes->>'previous_state' AS previous_state, attributes->>'new_state' AS new_state
		FROM domain_audit WHERE target_type = 'post' AND target_id = ?
		  AND attributes @> '{"changed_fields":["status"]}'::jsonb
		ORDER BY occurred_at, audit_id`, created.Msg.Id).Scan(&lifecycle).Error)
	require.Len(t, lifecycle, 3)
	require.Equal(t, string(sharedtelemetry.AuditStateDraft), lifecycle[0].PreviousState)
	require.Equal(t, string(sharedtelemetry.AuditStatePublished), lifecycle[0].NewState)
	require.Equal(t, string(sharedtelemetry.AuditStatePublished), lifecycle[1].PreviousState)
	require.Equal(t, string(sharedtelemetry.AuditStateArchived), lifecycle[1].NewState)
	require.Equal(t, string(sharedtelemetry.AuditStateArchived), lifecycle[2].PreviousState)
	require.Equal(t, string(sharedtelemetry.AuditStateDraft), lifecycle[2].NewState)
	_, err = service.UnpublishPost(adminCtx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "draft policy remains unchanged")
}
