//go:build integration

package filemedia

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFileServiceManageDownloadPolicyPersistsAudienceSegmentsUnit(t *testing.T) {
	db := newManageFileDownloadPolicyUnitDB(t)
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	postID := uuid.NewString()
	fileID := uuid.NewString()
	segmentID := uuid.NewString()
	archivedSegmentID := uuid.NewString()
	seedManageFileDownloadPolicyFile(
		t,
		db,
		fileID,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		postID,
	)
	blockID := managePolicyBlockID(t, db, fileID)
	seedManageAudienceSegment(
		t,
		db,
		segmentID,
		"Members",
		managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS,
		model.AudienceSegmentConfig{},
	)
	seedManageAudienceSegment(
		t,
		db,
		archivedSegmentID,
		"Archived members",
		managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS,
		model.AudienceSegmentConfig{},
	)
	require.NoError(t, db.Table("audience_segment").
		Where("id = ?", archivedSegmentID).
		Update("archived_at", time.Now().UTC()).Error)

	svc := &FileService{
		db: db, spiceDB: stack.SpiceDBClient,
	}
	WithPostAccess(newIntegrationPostAccess(db, stack.SpiceDBClient))(svc)
	WithAudienceAccess(newIntegrationAudienceAccess())(svc)
	seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, "post", postID)
	seedFileDeliveryPostAuthority(t, stack.SpiceDBClient, postID, manager.IdentityID)
	ctx := fileDeliveryPrincipalContext(manager)

	restrictedWithoutSegments, err := svc.UpdateFileDownloadPolicy(ctx, connect.NewRequest(
		&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   postID,
			BlockId:    managePolicyString(blockID), ReferencePath: managePolicyString("file"),
			ExpectedFileId: fileID,
			Audience:       managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED,
		},
	))
	require.NoError(t, err)
	require.Empty(t, restrictedWithoutSegments.Msg.GetPolicy().GetAudienceSegments())

	restricted, err := svc.UpdateFileDownloadPolicy(ctx, connect.NewRequest(
		&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   postID,
			BlockId:    managePolicyString(blockID), ReferencePath: managePolicyString("file"), ExpectedFileId: fileID,
			Audience:           managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED,
			AudienceSegmentIds: []string{segmentID, segmentID},
		},
	))
	require.NoError(t, err)
	require.Len(t, restricted.Msg.GetPolicy().GetAudienceSegments(), 1)
	require.Equal(t, segmentID, restricted.Msg.GetPolicy().GetAudienceSegments()[0].GetId())

	_, err = svc.UpdateFileDownloadPolicy(ctx, connect.NewRequest(
		&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   postID,
			BlockId:    managePolicyString(blockID), ReferencePath: managePolicyString("file"), ExpectedFileId: fileID,
			Audience:           managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC,
			AudienceSegmentIds: []string{segmentID},
		},
	))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = svc.UpdateFileDownloadPolicy(ctx, connect.NewRequest(
		&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   postID,
			BlockId:    managePolicyString(blockID), ReferencePath: managePolicyString("file"), ExpectedFileId: fileID,
			Audience:           managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED,
			AudienceSegmentIds: []string{archivedSegmentID},
		},
	))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	tooManySegmentIDs := make([]string, maxFileDownloadAudienceSegments+1)
	for i := range tooManySegmentIDs {
		tooManySegmentIDs[i] = uuid.NewString()
	}
	_, err = svc.UpdateFileDownloadPolicy(ctx, connect.NewRequest(
		&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   postID,
			BlockId:    managePolicyString(blockID), ReferencePath: managePolicyString("file"), ExpectedFileId: fileID,
			Audience:           managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED,
			AudienceSegmentIds: tooManySegmentIDs,
		},
	))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestFileDownloadPolicyScopeUsesCurrentBindingInsteadOfIngestProvenance(t *testing.T) {
	db := newManageFileDownloadPolicyUnitDB(t)
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	originalPostID := uuid.NewString()
	currentPostID := uuid.NewString()
	fileID := uuid.NewString()
	seedManageFileDownloadPolicyFile(
		t,
		db,
		fileID,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		originalPostID,
	)
	currentDocumentID := uuid.NewString()
	currentBlockID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, currentDocumentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, content_document_id) VALUES (?, ?)`, currentPostID, currentDocumentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, kind) VALUES (?, ?, 'file')`, currentBlockID, currentDocumentID).Error)
	require.NoError(t, db.Exec(`DELETE FROM content_block_attachment WHERE file_id = ?`, fileID).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id) VALUES (?, 'file', 'active', ?)`,
		currentBlockID,
		fileID,
	).Error)
	seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, "post", currentPostID)
	seedFileDeliveryPostAuthority(t, stack.SpiceDBClient, currentPostID, manager.IdentityID)

	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient}
	WithPostAccess(newIntegrationPostAccess(db, stack.SpiceDBClient))(svc)
	WithAudienceAccess(newIntegrationAudienceAccess())(svc)
	response, err := svc.UpdateFileDownloadPolicy(
		fileDeliveryPrincipalContext(manager),
		connect.NewRequest(&managev1.UpdateFileDownloadPolicyRequest{
			EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId:   currentPostID,
			BlockId:    managePolicyString(currentBlockID), ReferencePath: managePolicyString("file"), ExpectedFileId: fileID,
			Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC,
		}),
	)
	require.NoError(t, err)
	require.Equal(t, managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC, response.Msg.GetPolicy().GetAudience())
}

func TestAudienceServiceListSegmentsForAuthenticatedAccessUnit(t *testing.T) {
	db := newManageFileDownloadPolicyUnitDB(t)
	stack := testutil.SetupOryStack(t)
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	memberTagSegmentID := uuid.NewString()
	filterSegmentID := uuid.NewString()
	archivedSegmentID := uuid.NewString()
	seedManageAudienceSegment(
		t,
		db,
		memberTagSegmentID,
		"Tagged members",
		managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS,
		model.AudienceSegmentConfig{},
	)
	seedManageAudienceSegment(
		t,
		db,
		filterSegmentID,
		"Authors",
		managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
		model.AudienceSegmentConfig{AccountRoles: []string{"author"}},
	)
	seedManageAudienceSegment(
		t,
		db,
		archivedSegmentID,
		"Archived",
		managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
		model.AudienceSegmentConfig{},
	)
	require.NoError(t, db.Table("audience_segment").
		Where("id = ?", archivedSegmentID).
		Update("archived_at", time.Now().UTC()).Error)

	svc := newAudienceServiceForTest(
		db,
		stack.SpiceDBClient,
	)
	ctx := fileDeliveryPrincipalContext(author)
	response, err := svc.ListSegmentsForAuthenticatedAccess(
		ctx,
		connect.NewRequest(&managev1.ListSegmentsForAuthenticatedAccessRequest{}),
	)
	require.NoError(t, err)
	require.Equal(t, int32(2), response.Msg.GetPagination().GetTotal())
	require.Len(t, response.Msg.GetSegments(), 2)
	require.Equal(t, filterSegmentID, response.Msg.GetSegments()[0].GetId())
	require.Equal(t, memberTagSegmentID, response.Msg.GetSegments()[1].GetId())

	_, err = svc.ListSegmentsForAuthenticatedAccess(
		fileDeliveryPrincipalContext(user),
		connect.NewRequest(&managev1.ListSegmentsForAuthenticatedAccessRequest{}),
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func newManageFileDownloadPolicyUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS kratos`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE file (
			id TEXT PRIMARY KEY,
			file_name TEXT,
			extension TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			delete_requested_at DATETIME
		);
		CREATE TABLE file_ingest_binding (
			file_id TEXT PRIMARY KEY,
			upload_type TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT NOT NULL,
			created_at DATETIME
		);
		CREATE TABLE content_document (id TEXT PRIMARY KEY);
		CREATE TABLE post (id TEXT PRIMARY KEY, content_document_id TEXT, status TEXT NOT NULL DEFAULT 'draft');
		CREATE TABLE content_block (id TEXT PRIMARY KEY, document_id TEXT NOT NULL, kind TEXT NOT NULL);
		CREATE TABLE content_block_attachment (
			block_id TEXT NOT NULL,
			reference_path TEXT NOT NULL,
			selector_kind TEXT NOT NULL,
			file_id TEXT,
			missing_kind TEXT,
			download_audience TEXT NOT NULL DEFAULT 'disabled',
			PRIMARY KEY (block_id, reference_path)
		);
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			onboarded BOOLEAN NOT NULL DEFAULT TRUE,
			deleted_at DATETIME
		);
		CREATE TABLE kratos.identities (
			id TEXT PRIMARY KEY,
			external_id TEXT,
			state TEXT NOT NULL,
			metadata_public TEXT NOT NULL,
			metadata_admin TEXT NOT NULL
		);
		CREATE TABLE post_author (
			post_id TEXT NOT NULL,
			member_id TEXT NOT NULL,
			PRIMARY KEY (post_id, member_id)
		);
		CREATE TABLE post_collaborator (
			post_id TEXT NOT NULL,
			member_id TEXT NOT NULL,
			PRIMARY KEY (post_id, member_id)
		);
		CREATE TABLE release (id TEXT PRIMARY KEY);
		CREATE TABLE track (
			id TEXT PRIMARY KEY,
			release_id TEXT NOT NULL,
			audio_original_file_id TEXT,
			download_audience TEXT NOT NULL DEFAULT 'disabled'
		);
		CREATE TABLE audience_segment (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			segment_type TEXT NOT NULL,
			created_after DATETIME,
			created_before DATETIME,
			archived_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME
		);
		CREATE TABLE audience_segment_user_tag (
			audience_segment_id TEXT NOT NULL,
			user_tag_id TEXT NOT NULL,
			PRIMARY KEY (audience_segment_id, user_tag_id)
		);
		CREATE TABLE audience_segment_user_role (
			audience_segment_id TEXT NOT NULL,
			role TEXT NOT NULL,
			PRIMARY KEY (audience_segment_id, role)
		);
		CREATE TABLE audience_segment_excluded_member (
			audience_segment_id TEXT NOT NULL,
			member_id TEXT NOT NULL,
			PRIMARY KEY (audience_segment_id, member_id)
		);
		CREATE TABLE content_block_attachment_download_audience_segment (
			block_id TEXT NOT NULL,
			reference_path TEXT NOT NULL,
			audience_segment_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (block_id, reference_path, audience_segment_id)
		);
		CREATE TABLE track_download_audience_segment (
			track_id TEXT NOT NULL,
			audience_segment_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (track_id, audience_segment_id)
		)
	`).Error)
	return db
}

func seedManageFileDownloadPolicyFile(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO file (
			id, file_name, extension, mime_type, file_size
		) VALUES (?, 'source.wav', 'wav', 'audio/wav', 4096)`,
		fileID,
	).Error)
	entityTypeName := entityType.String()
	require.NoError(t, db.Exec(
		`INSERT INTO file_ingest_binding (
			file_id, upload_type, entity_type, entity_id, created_at
		) VALUES (?, ?, ?, ?, ?)`,
		fileID,
		uploadType.String(),
		entityTypeName,
		entityID,
		time.Now().UTC(),
	).Error)
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST {
		documentID := uuid.NewString()
		blockID := uuid.NewString()
		require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, documentID).Error)
		require.NoError(t, db.Exec(`INSERT INTO post (id, content_document_id) VALUES (?, ?)`, entityID, documentID).Error)
		require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, kind) VALUES (?, ?, 'file')`, blockID, documentID).Error)
		require.NoError(t, db.Exec(
			`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES (?, 'file', 'active', ?, 'authenticated')`,
			blockID,
			fileID,
		).Error)
	}
}

func managePolicyString(value string) *string { return &value }

func managePolicyBlockID(t *testing.T, db *gorm.DB, fileID string) string {
	t.Helper()
	var blockID string
	require.NoError(t, db.Table("content_block_attachment").Where("file_id = ?", fileID).Pluck("block_id", &blockID).Error)
	require.NotEmpty(t, blockID)
	return blockID
}

func seedManageAudienceSegment(
	t *testing.T,
	db *gorm.DB,
	id string,
	name string,
	segmentType managev1.SegmentType,
	config model.AudienceSegmentConfig,
) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type,
			created_after, created_before, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id,
		name,
		segmentType.String(),
		config.CreatedAfter,
		config.CreatedBefore,
		time.Now().UTC(),
		time.Now().UTC(),
	).Error)
	seedAudienceSegmentRelationsForTest(t, db, id, config)
}
