//go:build integration

package filemedia

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFileDownloadAudiencePolicyIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	authorIdentityID := uuid.NewString()
	authorMemberID := uuid.NewString()
	memberIdentityID := uuid.NewString()
	memberID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: authorIdentityID,
	})
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: memberIdentityID,
	})
	seedFileDownloadMember(t, db, authorIdentityID, authorMemberID, policyv1.Role.Author().ID())
	seedFileDownloadMember(t, db, memberIdentityID, memberID, policyv1.Role.User().ID())
	authorSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(authorIdentityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), authorSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{MemberID: authorMemberID})
	require.NoError(t, err)
	authorCtx := auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), &auth.UserInfo{
		IdentityID:    auth.IdentityID(authorIdentityID),
		MemberID:      auth.MemberID(authorMemberID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true, Onboarded: true,
	})
	tagID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag (id, name, created_at) VALUES (?::uuid, ?, NOW())`,
		tagID, "Download members "+uuid.NewString(),
	).Error)
	require.NoError(t, db.Create(&model.UserTagMapping{
		MemberID: memberID,
		TagID:    tagID,
	}).Error)

	segmentID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.AudienceSegment{
		ID: segmentID, Name: "Download audience " + uuid.NewString(),
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String(),
		CreatedAt:   now, UpdatedAt: &now,
	}).Error)
	require.NoError(t, db.Create(&model.AudienceSegmentUserTag{
		AudienceSegmentID: segmentID,
		UserTagID:         tagID,
	}).Error)

	fileID := uuid.NewString()
	postID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO file (
			id, file_name, mime_type, file_size, extension, sha256
		) VALUES (?, 'source', 'audio/wav', 4096, 'wav', ?)`,
		fileID,
		make([]byte, 32),
	).Error)
	seedAudiencePostFileAttachment(t, db, postID, fileID)
	var blockID string
	require.NoError(t, db.Table("content_block_attachment").Where("file_id = ?", fileID).Pluck("block_id", &blockID).Error)
	require.NotEmpty(t, blockID)
	require.NoError(t, db.Exec(`INSERT INTO post_author (post_id, member_id) VALUES (?, ?)`, postID, authorMemberID).Error)
	postPolicy, err := policyv1.Post.TouchPolicy(postID)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), postPolicy)
	require.NoError(t, err)

	fileSvc := &FileService{
		db:          db,
		spiceDB:     stack.SpiceDBClient,
		auditWriter: apitelemetry.NewDurableWriter(db),
	}
	WithPostAccess(newIntegrationPostAccess(db, stack.SpiceDBClient))(fileSvc)
	WithAudienceAccess(newIntegrationAudienceAccess())(fileSvc)
	updated, err := fileSvc.UpdateFileDownloadPolicy(
		authorCtx,
		connect.NewRequest(&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   postID,
			BlockId:    managePolicyString(blockID), ReferencePath: managePolicyString("file"), ExpectedFileId: fileID,
			Audience:           managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED,
			AudienceSegmentIds: []string{segmentID},
		}),
	)
	require.NoError(t, err)
	require.Len(t, updated.Msg.Policy.AudienceSegments, 1)

	sources, err := mediaasset.LoadContentBlockDownloadSources(context.Background(), db, []mediaasset.ContentBlockDownloadSelector{{BlockID: blockID, ReferencePath: "file"}})
	require.NoError(t, err)
	source := sources[mediaasset.ContentBlockDownloadPolicyKey(blockID, "file")]
	allowed, err := mediaasset.EvaluateFileDownloadAccess(
		context.Background(),
		db,
		stack.SpiceDBClient,
		source,
		&auth.UserInfo{
			IdentityID:    auth.IdentityID(memberIdentityID),
			MemberID:      auth.MemberID(memberID),
			SessionID:     auth.SessionID(uuid.NewString()),
			Authenticated: true,
		},
		mediaassetadapter.NewSegmentConfigs(),
	)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, db.Where("member_id = ?", memberID).
		Delete(&model.UserTagMapping{}).Error)
	allowed, err = mediaasset.EvaluateFileDownloadAccess(
		context.Background(),
		db,
		stack.SpiceDBClient,
		source,
		&auth.UserInfo{
			IdentityID:    auth.IdentityID(memberIdentityID),
			MemberID:      auth.MemberID(memberID),
			SessionID:     auth.SessionID(uuid.NewString()),
			Authenticated: true,
		},
		mediaassetadapter.NewSegmentConfigs(),
	)
	require.NoError(t, err)
	require.False(t, allowed)

	zeroSegmentPolicy, err := fileSvc.UpdateFileDownloadPolicy(
		authorCtx,
		connect.NewRequest(&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   postID,
			BlockId:    managePolicyString(blockID), ReferencePath: managePolicyString("file"), ExpectedFileId: fileID,
			Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED,
		}),
	)
	require.NoError(t, err)
	require.Empty(t, zeroSegmentPolicy.Msg.Policy.AudienceSegments)
	var auditRows []postSeriesAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE target_type = 'post' AND target_id = ? ORDER BY occurred_at, audit_id`, postID).Scan(&auditRows).Error)
	require.Len(t, auditRows, 2)
	for _, row := range auditRows {
		require.Equal(t, "post.updated", row.Action)
		require.Equal(t, authorMemberID, row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(authorCtx), row.RequestID)
	}
	require.JSONEq(t, `{"changed_fields":["file_download_audience","file_download_audience_segment_ids"],"item_id":"`+blockID+`","file_id":"`+fileID+`","previous_state":"disabled","new_state":"restricted","previous_item_ids":[],"item_ids":["`+segmentID+`"]}`, string(auditRows[0].Attributes))
	require.JSONEq(t, `{"changed_fields":["file_download_audience_segment_ids"],"item_id":"`+blockID+`","file_id":"`+fileID+`","previous_item_ids":["`+segmentID+`"],"item_ids":[]}`, string(auditRows[1].Attributes))
	allowed, err = mediaasset.EvaluateFileDownloadAccess(
		context.Background(),
		db,
		stack.SpiceDBClient,
		source,
		&auth.UserInfo{
			IdentityID:    auth.IdentityID(memberIdentityID),
			MemberID:      auth.MemberID(memberID),
			SessionID:     auth.SessionID(uuid.NewString()),
			Authenticated: true,
		},
		mediaassetadapter.NewSegmentConfigs(),
	)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestGetFileDownloadPolicyLocksPostAndProgramEventInRepeatableReadIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID})
	seedFileDownloadMember(t, db, identityID, memberID, policyv1.Role.Admin().ID())
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})

	postID, postFileID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, 'post-source', 'text/plain', 1, 'txt', ?)`, postFileID, make([]byte, 32)).Error)
	seedAudiencePostFileAttachment(t, db, postID, postFileID)
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

	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient}
	WithPostAccess(newIntegrationPostAccess(db, stack.SpiceDBClient))(svc)
	WithProgramEventAttachment(newIntegrationProgramEventAccess(db))(svc)
	WithAudienceAccess(newIntegrationAudienceAccess())(svc)
	for _, request := range []*managev1.GetFileDownloadPolicyRequest{
		{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, BlockId: managePolicyString(postBlockID), ReferencePath: managePolicyString("file")},
		{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT, EntityId: eventID, BlockId: managePolicyString(eventBlockID), ReferencePath: managePolicyString("file")},
	} {
		response, getErr := svc.GetFileDownloadPolicy(ctx, connect.NewRequest(request))
		require.NoError(t, getErr)
		require.Equal(t, managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_DISABLED, response.Msg.GetPolicy().GetAudience())
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
