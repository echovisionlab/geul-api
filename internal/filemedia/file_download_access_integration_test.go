//go:build integration

package filemedia

import (
	"context"
	"testing"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"connectrpc.com/connect"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// restrictedDownloadFixture is a Post File attachment owned by an Author, and
// a Member who matches one Member criteria through a tag.
type restrictedDownloadFixture struct {
	db               *gorm.DB
	stack            *orytest.OryStack
	fileSvc          *Service
	authorCtx        context.Context
	authorMemberID   string
	memberID         string
	memberIdentityID string
	criteriaID       string
	postID, fileID   string
	blockID          string
}

func newRestrictedDownloadFixture(t *testing.T) restrictedDownloadFixture {
	t.Helper()
	f := restrictedDownloadFixture{
		db: orytest.NewIntegrationDB(t), stack: orytest.SetupOryStack(t),
		authorMemberID: uuid.NewString(), memberID: uuid.NewString(), memberIdentityID: uuid.NewString(),
		criteriaID: uuid.NewString(), postID: uuid.NewString(), fileID: uuid.NewString(),
	}
	authorIdentityID := uuid.NewString()
	orytest.SeedKratosIdentityFixture(t, f.db, orytest.KratosIdentityFixture{ID: authorIdentityID})
	orytest.SeedKratosIdentityFixture(t, f.db, orytest.KratosIdentityFixture{ID: f.memberIdentityID})
	seedFileDownloadMember(t, f.db, authorIdentityID, f.authorMemberID, policyv1.Role.Author().ID())
	seedFileDownloadMember(t, f.db, f.memberIdentityID, f.memberID, policyv1.Role.User().ID())
	authorSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(authorIdentityID))
	require.NoError(t, err)
	_, err = f.stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), authorSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{MemberID: f.authorMemberID})
	require.NoError(t, err)
	f.authorCtx = auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(authorIdentityID), MemberID: auth.MemberID(f.authorMemberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})

	tagID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, f.db.Exec(`INSERT INTO user_tag (id, name, created_at) VALUES (?::uuid, ?, NOW())`, tagID, "Download members "+uuid.NewString()).Error)
	require.NoError(t, f.db.Create(&model.UserTagMapping{MemberID: f.memberID, TagID: tagID}).Error)
	require.NoError(t, f.db.Create(&model.MemberCriteria{
		ID: f.criteriaID, Name: "Download access " + uuid.NewString(), Kind: model.MemberCriteriaKindMemberTags, CreatedAt: now, UpdatedAt: &now,
	}).Error)
	require.NoError(t, f.db.Create(&model.MemberCriteriaTag{MemberCriteriaID: f.criteriaID, UserTagID: tagID}).Error)

	require.NoError(t, f.db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, 'source', 'audio/wav', 4096, 'wav', ?)`,
		f.fileID, make([]byte, 32),
	).Error)
	seedAccessPostFileAttachment(t, f.db, f.postID, f.fileID)
	require.NoError(t, f.db.Table("content_block_attachment").Where("file_id = ?", f.fileID).Pluck("block_id", &f.blockID).Error)
	require.NotEmpty(t, f.blockID)
	require.NoError(t, f.db.Exec(`INSERT INTO post_author (post_id, member_id) VALUES (?, ?)`, f.postID, f.authorMemberID).Error)
	postPolicy, err := policyv1.Post.TouchPolicy(f.postID)
	require.NoError(t, err)
	_, err = f.stack.SpiceDBClient.ApplyRelationships(t.Context(), postPolicy)
	require.NoError(t, err)

	f.fileSvc = withTestMutations(withNativeFileUnits(&Service{spiceDB: f.stack.SpiceDBClient, auditWriter: apitelemetry.NewDurableWriter(f.db), asyncPublisher: &policySignalPublisher{}}, f.db, nativeFilePorts{Posts: newIntegrationPostAccess(f.db, f.stack.SpiceDBClient)}))
	return f
}

// restrict sets the attachment to restricted access with criteriaIDs, after observing previous.
func (f restrictedDownloadFixture) restrict(t *testing.T, previous *managev1.FileDownloadPolicyObservedState, criteriaIDs ...string) *managev1.FileDownloadPolicy {
	t.Helper()
	updated, err := NewConnectHandler(f.fileSvc).UpdateFileDownloadPolicy(f.authorCtx, connect.NewRequest(&managev1.UpdateFileDownloadPolicyRequest{
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: f.postID,
		BlockId: managePolicyString(f.blockID), ReferencePath: managePolicyString("file"), ExpectedFileId: f.fileID,
		Access: managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED, MemberCriteriaIds: criteriaIDs, ObservedPolicy: previous,
	}))
	require.NoError(t, err)
	return updated.Msg.Policy
}

// memberMayDownload evaluates the attachment's current policy for the fixture Member.
func (f restrictedDownloadFixture) memberMayDownload(t *testing.T) bool {
	t.Helper()
	sources, err := mediaasset.LoadContentBlockDownloadSources(context.Background(), mediaassetpostgres.NewDownloadStore(f.db, nil), []mediaasset.ContentBlockDownloadSelector{{BlockID: f.blockID, ReferencePath: "file"}})
	require.NoError(t, err)
	allowed, err := mediaasset.EvaluateFileDownloadAccess(context.Background(), mediaassetpostgres.NewDownloadStore(f.db, mediaassetadapter.NewCriteriaConfigs()), f.stack.SpiceDBClient,
		sources[mediaasset.ContentBlockDownloadPolicyKey(f.blockID, "file")], &auth.UserInfo{
			IdentityID: auth.IdentityID(f.memberIdentityID), MemberID: auth.MemberID(f.memberID), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
		})
	require.NoError(t, err)
	return allowed
}

func TestFileDownloadAccessPolicyIntegration(t *testing.T) {
	t.Parallel()
	f := newRestrictedDownloadFixture(t)

	policy := f.restrict(t, observedFileDownloadPolicy(managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_DISABLED), f.criteriaID)
	require.Len(t, policy.MemberCriteria, 1)
	require.True(t, f.memberMayDownload(t), "a Member matching the criteria tag may download")

	require.NoError(t, f.db.Where("member_id = ?", f.memberID).Delete(&model.UserTagMapping{}).Error)
	require.False(t, f.memberMayDownload(t), "removing the tag removes the grant")

	policy = f.restrict(t, observedFileDownloadPolicy(managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED, f.criteriaID))
	require.Empty(t, policy.MemberCriteria)
	require.False(t, f.memberMayDownload(t), "restricted access without criteria denies everyone")

	var auditRows []postSeriesAuditRow
	require.NoError(t, f.db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE target_type = 'post' AND target_id = ? ORDER BY occurred_at, audit_id`, f.postID).Scan(&auditRows).Error)
	require.Len(t, auditRows, 2)
	for _, row := range auditRows {
		require.Equal(t, "post.updated", row.Action)
		require.Equal(t, f.authorMemberID, row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(f.authorCtx), row.RequestID)
	}
	require.JSONEq(t, `{"changed_fields":["file_download_access","file_download_member_criteria_ids"],"item_id":"`+f.blockID+`","file_id":"`+f.fileID+`","previous_state":"disabled","new_state":"restricted","previous_item_ids":[],"item_ids":["`+f.criteriaID+`"]}`, string(auditRows[0].Attributes))
	require.JSONEq(t, `{"changed_fields":["file_download_member_criteria_ids"],"item_id":"`+f.blockID+`","file_id":"`+f.fileID+`","previous_item_ids":["`+f.criteriaID+`"],"item_ids":[]}`, string(auditRows[1].Attributes))
}

func TestGetFileDownloadPolicyLocksPostAndProgramEventInRepeatableReadIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewIntegrationDB(t)
	stack := orytest.SetupOryStack(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	orytest.SeedKratosIdentityFixture(t, db, orytest.KratosIdentityFixture{ID: identityID})
	seedFileDownloadMember(t, db, identityID, memberID, policyv1.Role.Admin().ID())
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
	ctx := auditedUserContext(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})

	postID, postFileID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, 'post-source', 'text/plain', 1, 'txt', ?)`, postFileID, make([]byte, 32)).Error)
	seedAccessPostFileAttachment(t, db, postID, postFileID)
	var postBlockID string
	require.NoError(t, db.Table("content_block_attachment").Where("file_id = ?", postFileID).Pluck("block_id", &postBlockID).Error)
	postPolicy, err := policyv1.Post.TouchPolicy(postID)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), postPolicy)
	require.NoError(t, err)

	eventID, eventFileID, eventTypeID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	documentID, eventBlockID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, 'event-source', 'text/plain', 1, 'txt', ?)`, eventFileID, make([]byte, 32)).Error)
	require.NoError(t, db.Exec(`INSERT INTO program_event_type (id, slug, status) VALUES (?, ?, 'PROGRAM_EVENT_TYPE_STATUS_ACTIVE')`, eventTypeID, "download-policy-"+eventTypeID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'program_event', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO program_event (id, slug, status, type_id, starts_at, timezone, location_mode, content_document_id) VALUES (?, ?, 'PROGRAM_EVENT_STATUS_DRAFT', ?, NOW(), 'UTC', 'PROGRAM_EVENT_LOCATION_MODE_ONLINE', ?)`, eventID, "download-policy-"+eventID, eventTypeID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)`, eventBlockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id) VALUES (?::uuid, 'file', 'active', ?::uuid)`, eventBlockID, eventFileID).Error)
	eventPolicy, err := policyv1.ProgramEvent.TouchPolicy(eventID)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), eventPolicy)
	require.NoError(t, err)

	svc := withTestMutations(withNativeFileUnits(&Service{spiceDB: stack.SpiceDBClient}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, stack.SpiceDBClient), Events: newIntegrationProgramEventAccess(db)}))

	for _, request := range []*managev1.GetFileDownloadPolicyRequest{
		{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, BlockId: managePolicyString(postBlockID), ReferencePath: managePolicyString("file")},
		{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT, EntityId: eventID, BlockId: managePolicyString(eventBlockID), ReferencePath: managePolicyString("file")},
	} {
		response, getErr := NewConnectHandler(svc).GetFileDownloadPolicy(ctx, connect.NewRequest(request))
		require.NoError(t, getErr)
		require.Equal(t, managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_DISABLED, response.Msg.GetPolicy().GetAccess())
	}
}

func seedFileDownloadMember(t *testing.T, db *gorm.DB, identityID, memberID, role string) {
	t.Helper()
	email := identityID + "@example.test"
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO account_identity (id, created_at)
		SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
		ON CONFLICT (id) DO NOTHING
	`, identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?::uuid, ?, TRUE, ?, string_to_array(?, ','))
	`, memberID, identityID, role+" member", email, email).Error)
}
