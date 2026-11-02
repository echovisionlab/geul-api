//go:build integration

package post_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestPostDocumentMetadataAuditAndRevisionCommitTogetherIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewPostIntegrationDB(t)
	identityID := orytest.PostIntegrationUUID()
	memberID := orytest.SeedPostIntegrationIdentity(t, db, identityID, "Post metadata contributor")
	spiceDB := orytest.PostIntegrationSpiceDB(t)
	orytest.GrantPostIntegrationRole(t, spiceDB, identityID, policyv1.Role.Admin())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	service := postintegration.NewService(t, postintegration.Deps{DB: db, SpiceDB: spiceDB, Signals: publisher})
	handler := post.NewConnectHandler(service)
	created, err := handler.CreatePost(withPostAuditedRequestContext(t, orytest.PostIntegrationContext(identityID)), connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Post metadata audit", Document: contenttest.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	postID := created.Msg.Id
	revision := func() string {
		var value string
		require.NoError(t, db.Raw("SELECT d.revision::text FROM content_document d JOIN post p ON p.content_document_id = d.id WHERE p.id = ?", postID).Scan(&value).Error)
		return value
	}
	categoryID, tagID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO category (id, name, slug) VALUES (?, ?, ?)", categoryID, "Metadata category", "category-"+categoryID).Error)
	require.NoError(t, db.Exec("INSERT INTO tag (id, name, slug) VALUES (?, ?, ?)", tagID, "Metadata tag", "tag-"+tagID).Error)
	beforeEvents := len(contenttest.ContentUpdatedEvents(t, db, postID))
	beforeAudit := len(contenttest.LoadDomainAuditRows(t, db, "post", postID))
	command := post.UpdatePostDocumentMetadataCommand{
		PostID: postID, Locale: "en", ExpectedRevision: revision(), ContributorMemberIDs: []string{memberID},
		CategoryIDs: &[]string{categoryID}, TagIDs: &[]string{tagID},
	}
	saved, err := service.UpdatePostDocumentMetadata(postAuditedSystemRequestContext(t), command)
	require.NoError(t, err)
	require.True(t, saved.Changed)
	require.NotEqual(t, command.ExpectedRevision, revision())
	require.Equal(t, saved.DocumentRevision, revision())
	records := contenttest.LoadDomainAuditRows(t, db, "post", postID)
	require.Len(t, records, beforeAudit+1)
	record := records[beforeAudit]
	require.Equal(t, string(sharedtelemetry.AuditPostUpdated), record.Action)
	require.Equal(t, string(sharedtelemetry.ActorKindMember), record.ActorKind)
	require.Equal(t, &memberID, record.ActorMemberID)
	require.EqualValues(t, []string{"categories", "tags"}, record.ChangedFields)
	events := contenttest.ContentUpdatedEvents(t, db, postID)[beforeEvents:]
	require.Len(t, events, 1)
	event := events[0]
	require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, event.GetSource())
	require.Equal(t, saved.DocumentRevision, event.GetDocumentRevision())
	require.Equal(t, "en", event.GetLocale())
	require.True(t, event.GetDocumentStateChanged())
	require.Equal(t, []string{memberID}, event.GetContributorMemberIds())
	require.Equal(t, []*managev1.ContentUpdatedField{
		{Path: "relations.categories", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
		{Path: "relations.tags", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	}, event.GetChangedFields())

	command.ExpectedRevision = saved.DocumentRevision
	unchanged, err := service.UpdatePostDocumentMetadata(postAuditedSystemRequestContext(t), command)
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, saved.DocumentRevision, revision())
	require.Len(t, contenttest.LoadDomainAuditRows(t, db, "post", postID), beforeAudit+1)
	require.Len(t, contenttest.ContentUpdatedEvents(t, db, postID), beforeEvents+1)
	require.NoError(t, db.Exec(`CREATE FUNCTION reject_post_metadata_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'metadata audit rejected'; RETURN NEW; END $$`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_post_metadata_audit BEFORE INSERT ON public.domain_audit
 FOR EACH ROW EXECUTE FUNCTION reject_post_metadata_audit()`).Error)
	command.CategoryIDs = &[]string{}
	_, err = service.UpdatePostDocumentMetadata(postAuditedSystemRequestContext(t), command)
	var auditFailure *pgconn.PgError
	require.ErrorAs(t, err, &auditFailure)
	require.Equal(t, "P0001", auditFailure.Code)
	require.Equal(t, saved.DocumentRevision, revision())
	var categories, tags []string
	require.NoError(t, db.Table("post_category").Where("post_id = ?", postID).Pluck("category_id", &categories).Error)
	require.NoError(t, db.Table("post_tag").Where("post_id = ?", postID).Pluck("tag_id", &tags).Error)
	require.Equal(t, []string{categoryID}, categories)
	require.Equal(t, []string{tagID}, tags)
	require.Len(t, contenttest.LoadDomainAuditRows(t, db, "post", postID), beforeAudit+1)
	require.Len(t, contenttest.ContentUpdatedEvents(t, db, postID), beforeEvents+1)
}
