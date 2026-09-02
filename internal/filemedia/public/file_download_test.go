package public

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const publicDownloadUnitSecret = "public-download-unit-secret"

func TestFileServiceAuthorizeDownloadPostAudienceAndScopeUnit(t *testing.T) {
	db := newPublicFileDownloadUnitDB(t)
	postID := uuid.NewString()
	fileID := uuid.NewString()
	fileName := "간월재 원본.wav"
	seedPublicDownloadPost(t, db, postID, managev1.PostStatus_POST_STATUS_PUBLISHED, fileID)
	seedPublicDownloadFile(t, db, fileID, fileName, "wav", "audio/wav", mediaasset.FileDownloadAudienceAuthenticated)

	svc := NewFileService(
		db,
		&auth.SpiceDBClient{},
		"https://cdn.example.com",
		"https://media.example.com",
		publicDownloadUnitSecret,
		24*time.Hour,
		WithDownloadSegmentConfigs(mediaassetadapter.NewSegmentConfigs()),
	)
	anonymous := context.Background()
	memberID := uuid.NewString()
	memberIdentityID := uuid.NewString()
	adminID := uuid.NewString()
	adminIdentityID := uuid.NewString()
	memberCtx := publicDownloadMemberContext(memberID, memberIdentityID, false)
	adminCtx := publicDownloadMemberContext(adminID, adminIdentityID, false)
	require.NoError(t, db.Exec(
		`INSERT INTO kratos.identities (
			id, external_id, state, traits, metadata_public, metadata_admin, created_at
		) VALUES
			(?, ?, 'active', '{}', '{}', '{"banned":false}', ?),
			(?, ?, 'active', '{}', '{}', '{"banned":false}', ?)`,
		memberIdentityID,
		memberID,
		time.Now().UTC(),
		adminIdentityID,
		adminID,
		time.Now().UTC(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO member (id, account_identity_id, nickname, onboarded, created_at)
		 VALUES (?, ?, 'Member', 1, ?), (?, ?, 'Admin', 1, ?)`,
		memberID,
		memberIdentityID,
		time.Now().UTC(),
		adminID,
		adminIdentityID,
		time.Now().UTC(),
	).Error)

	response := authorizePostDownload(t, svc, anonymous, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_SIGN_IN,
		false,
	)

	response = authorizePostDownload(t, svc, memberCtx, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD,
		true,
	)
	assertDownloadToken(t, response.GetDownload().GetUrl(), fileID, fileName, mediaauth.DownloadTTL)

	response = authorizePostDownload(
		t,
		svc,
		publicDownloadMemberContext(memberID, memberIdentityID, true),
		postID,
		fileID,
		nil,
	)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)

	setPublicDownloadAudience(t, db, fileID, mediaasset.FileDownloadAudienceDisabled)
	response = authorizePostDownload(t, svc, adminCtx, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)

	setPublicDownloadAudience(t, db, fileID, mediaasset.FileDownloadAudiencePublic)
	response = authorizePostDownload(t, svc, anonymous, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD,
		true,
	)

	tagID := uuid.NewString()
	segmentID := uuid.NewString()
	expiredMemberID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag (id, name, created_at) VALUES (?, 'Members', ?)`,
		tagID,
		time.Now().UTC(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag_mapping (member_id, tag_id, created_at) VALUES (?, ?, ?)`,
		memberID,
		tagID,
		time.Now().UTC(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type, created_at, updated_at
		) VALUES (?, 'Members', 'SEGMENT_TYPE_MEMBER_TAGS', ?, ?)`,
		segmentID,
		time.Now().UTC(),
		time.Now().UTC(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment_user_tag (
			audience_segment_id, user_tag_id
		) VALUES (?, ?)`,
		segmentID,
		tagID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment_download_audience_segment (
			block_id, reference_path, audience_segment_id, created_at
		)
		SELECT block_id, reference_path, ?, ?
		FROM content_block_attachment WHERE file_id = ?
	`, segmentID, time.Now().UTC(), fileID).Error)
	setPublicDownloadAudience(t, db, fileID, mediaasset.FileDownloadAudienceRestricted)

	response = authorizePostDownload(t, svc, anonymous, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_SIGN_IN,
		false,
	)
	response = authorizePostDownload(t, svc, memberCtx, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD,
		true,
	)
	response = authorizePostDownload(
		t,
		svc,
		publicDownloadMemberContext(expiredMemberID, uuid.NewString(), false),
		postID,
		fileID,
		nil,
	)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)
	response = authorizePostDownload(
		t,
		svc,
		publicDownloadMemberContext(uuid.NewString(), uuid.NewString(), false),
		postID,
		fileID,
		nil,
	)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)
	response = authorizePostDownload(t, svc, adminCtx, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)

	require.NoError(t, db.Exec(
		`DELETE FROM user_tag_mapping
		  WHERE tag_id = ? AND member_id = ?`,
		tagID,
		memberID,
	).Error)
	response = authorizePostDownload(t, svc, memberCtx, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)

	require.NoError(t, db.Exec(`
		DELETE FROM content_block_attachment_download_audience_segment
		WHERE block_id IN (SELECT block_id FROM content_block_attachment WHERE file_id = ?)
	`, fileID).Error)
	response = authorizePostDownload(t, svc, anonymous, postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)
}

func TestAuthorizedPostDownloadRejectsDetachedFilesUnit(t *testing.T) {
	db := newPublicFileDownloadUnitDB(t)
	publishedPostID := uuid.NewString()
	otherPostID := uuid.NewString()
	fileID := uuid.NewString()
	detachedFileID := uuid.NewString()

	seedPublicDownloadPost(t, db, publishedPostID, managev1.PostStatus_POST_STATUS_PUBLISHED, fileID)
	seedPublicDownloadPost(t, db, otherPostID, managev1.PostStatus_POST_STATUS_PUBLISHED, "")
	seedPublicDownloadFile(t, db, fileID, "public.wav", "wav", "audio/wav", mediaasset.FileDownloadAudiencePublic)
	seedPublicDownloadFile(t, db, detachedFileID, "detached.wav", "wav", "audio/wav", mediaasset.FileDownloadAudiencePublic)

	svc := NewFileService(db, &auth.SpiceDBClient{}, "cdn.example.com", "media.example.com", publicDownloadUnitSecret, time.Minute)
	for name, scope := range map[string][2]string{
		"wrong entity":  {otherPostID, fileID},
		"detached file": {publishedPostID, detachedFileID},
		"missing post":  {uuid.NewString(), fileID},
	} {
		t.Run(name, func(t *testing.T) {
			response := authorizePostDownload(t, svc, context.Background(), scope[0], scope[1], nil)
			assertFileDownloadAccess(
				t,
				response,
				openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
				openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
				false,
			)
		})
	}

	response := authorizePostDownload(t, svc, context.Background(), publishedPostID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD,
		true,
	)
}

func TestFileServiceAuthorizeDownloadUsesExactReleaseTrackRelationUnit(t *testing.T) {
	db := newPublicFileDownloadUnitDB(t)
	releaseID := uuid.NewString()
	trackID := uuid.NewString()
	fileID := uuid.NewString()
	seedPublicDownloadFile(t, db, fileID, "original.wav", "wav", "audio/wav", mediaasset.FileDownloadAudienceDisabled)
	require.NoError(t, db.Exec(`INSERT INTO release (id, status) VALUES (?, ?)`, releaseID, managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()).Error)
	require.NoError(t, db.Exec(`INSERT INTO track (id, release_id, audio_original_file_id, download_audience) VALUES (?, ?, ?, 'public')`, trackID, releaseID, fileID).Error)
	svc := NewFileService(db, &auth.SpiceDBClient{}, "cdn.example.com", "media.example.com", publicDownloadUnitSecret, time.Minute)
	response, err := svc.AuthorizeDownload(context.Background(), connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType:     openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_RELEASE,
		EntityId:       releaseID,
		RelationTarget: &openv1.AuthorizeDownloadRequest_TrackId{TrackId: trackID},
	}))
	require.NoError(t, err)
	assertFileDownloadAccess(t, response.Msg, openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD, true)

	wrongOwner, err := svc.AuthorizeDownload(context.Background(), connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType:     openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_RELEASE,
		EntityId:       uuid.NewString(),
		RelationTarget: &openv1.AuthorizeDownloadRequest_TrackId{TrackId: trackID},
	}))
	require.NoError(t, err)
	assertFileDownloadAccess(t, wrongOwner.Msg, openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, false)
}

func TestAuthorizedPostDownloadRejectsDraftWithoutAuthenticatedViewUnit(t *testing.T) {
	db := newPublicFileDownloadUnitDB(t)
	postID := uuid.NewString()
	fileID := uuid.NewString()
	seedPublicDownloadPost(t, db, postID, managev1.PostStatus_POST_STATUS_DRAFT, fileID)
	seedPublicDownloadFile(t, db, fileID, "draft.wav", "wav", "audio/wav", mediaasset.FileDownloadAudiencePublic)
	svc := NewFileService(
		db,
		&auth.SpiceDBClient{},
		"cdn.example.com",
		"media.example.com",
		publicDownloadUnitSecret,
		time.Hour,
	)

	response := authorizePostDownload(
		t,
		svc,
		context.Background(),
		postID,
		fileID,
		nil,
	)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
		false,
	)
}

func TestFileServiceAuthorizeDownloadValidatesEntityAndFileIDsUnit(t *testing.T) {
	svc := NewFileService(newPublicFileDownloadUnitDB(t), &auth.SpiceDBClient{}, "", "", publicDownloadUnitSecret, time.Minute)
	for name, testCase := range map[string]struct {
		request *openv1.AuthorizeDownloadRequest
		code    connect.Code
	}{
		"missing entity": {
			request: &openv1.AuthorizeDownloadRequest{RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{BlockId: uuid.NewString(), ReferencePath: "file"}}},
			code:    connect.CodeInvalidArgument,
		},
		"invalid entity": {
			request: &openv1.AuthorizeDownloadRequest{EntityId: "post-1", RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{BlockId: uuid.NewString(), ReferencePath: "file"}}},
			code:    connect.CodeInvalidArgument,
		},
		"missing selector": {
			request: &openv1.AuthorizeDownloadRequest{EntityId: uuid.NewString()},
			code:    connect.CodeInvalidArgument,
		},
		"invalid block": {
			request: &openv1.AuthorizeDownloadRequest{EntityId: uuid.NewString(), RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{BlockId: "block-1", ReferencePath: "file"}}},
			code:    connect.CodeInvalidArgument,
		},
		"missing reference path": {
			request: &openv1.AuthorizeDownloadRequest{EntityId: uuid.NewString(), RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{BlockId: uuid.NewString()}}},
			code:    connect.CodeInvalidArgument,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.AuthorizeDownload(context.Background(), connect.NewRequest(testCase.request))
			require.Error(t, err)
			assert.Equal(t, testCase.code, connect.CodeOf(err))
		})
	}
}

func TestFileServiceAuthorizeDownloadUsesFallbackForHistoricalInvalidFilenameUnit(t *testing.T) {
	db := newPublicFileDownloadUnitDB(t)
	postID := uuid.NewString()
	fileID := uuid.NewString()
	seedPublicDownloadPost(t, db, postID, managev1.PostStatus_POST_STATUS_PUBLISHED, fileID)
	seedPublicDownloadFile(
		t,
		db,
		fileID,
		"../source.wav",
		"wav",
		"audio/wav",
		mediaasset.FileDownloadAudiencePublic,
	)
	svc := NewFileService(db, &auth.SpiceDBClient{}, "cdn.example.com", "media.example.com", publicDownloadUnitSecret, time.Minute)

	response := authorizePostDownload(t, svc, context.Background(), postID, fileID, nil)
	assertFileDownloadAccess(
		t,
		response,
		openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
		openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD,
		true,
	)
	assertDownloadToken(t, response.GetDownload().GetUrl(), fileID, "download-"+fileID+".wav", time.Minute)
}

func newPublicFileDownloadUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS kratos`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE content_document (id TEXT PRIMARY KEY);
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
		CREATE TABLE post (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			content_document_id TEXT,
			featured_image_file_id TEXT
		);
		CREATE TABLE release (id TEXT PRIMARY KEY, status TEXT NOT NULL);
		CREATE TABLE track (
			id TEXT PRIMARY KEY,
			release_id TEXT NOT NULL,
			audio_original_file_id TEXT,
			download_audience TEXT NOT NULL DEFAULT 'disabled'
		);
		CREATE TABLE track_download_audience_segment (
			track_id TEXT NOT NULL,
			audience_segment_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (track_id, audience_segment_id)
		);
		CREATE TABLE file (
			id TEXT PRIMARY KEY,
			file_name TEXT,
			extension TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			delete_requested_at DATETIME
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
		CREATE TABLE user_tag (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE user_tag_mapping (
			member_id TEXT NOT NULL,
			tag_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (member_id, tag_id)
		);
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			nickname TEXT,
			onboarded INTEGER NOT NULL DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE kratos.identities (
			id TEXT PRIMARY KEY,
			external_id TEXT,
			state TEXT NOT NULL,
			traits TEXT NOT NULL,
			metadata_public TEXT NOT NULL,
			metadata_admin TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE file_derivative (
			file_id TEXT NOT NULL,
			type TEXT NOT NULL,
			asset_id TEXT,
			media_generation_id TEXT
		);
		CREATE TABLE public_asset (
			id TEXT PRIMARY KEY,
			source_file_id TEXT,
			kind TEXT NOT NULL,
			extension TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			sha256 BLOB NOT NULL,
			disposition TEXT NOT NULL,
			download_filename TEXT,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)
	`).Error)
	return db
}

func seedPublicDownloadPost(
	t *testing.T,
	db *gorm.DB,
	postID string,
	status managev1.PostStatus,
	fileID string,
) {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, documentID).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, status, content_document_id) VALUES (?, ?, ?)`,
		postID, status.String(), documentID,
	).Error)
	if fileID != "" {
		blockID := uuid.NewString()
		require.NoError(t, db.Exec(
			`INSERT INTO content_block (id, document_id, kind) VALUES (?, ?, 'file')`,
			blockID, documentID,
		).Error)
		require.NoError(t, db.Exec(
			`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
			 VALUES (?, 'file', 'active', ?)`,
			blockID, fileID,
		).Error)
	}
}

func seedPublicDownloadFile(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	fileName string,
	extension string,
	mimeType string,
	audience mediaasset.FileDownloadAudience,
) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, extension, mime_type, file_size)
		 VALUES (?, ?, ?, ?, 4096)`,
		fileID,
		fileName,
		extension,
		mimeType,
	).Error)
	setPublicDownloadAudience(t, db, fileID, audience)
}

func setPublicDownloadAudience(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	audience mediaasset.FileDownloadAudience,
) {
	t.Helper()
	require.NoError(t, db.Exec(
		`UPDATE content_block_attachment SET download_audience = ? WHERE file_id = ?`,
		string(audience),
		fileID,
	).Error)
}

func authorizePostDownload(
	t *testing.T,
	svc *FileService,
	ctx context.Context,
	postID string,
	fileID string,
	shareToken *string,
) *openv1.AuthorizeDownloadResponse {
	t.Helper()
	_ = shareToken
	selector := &contentv1.ContentBlockMediaSelector{BlockId: uuid.NewString(), ReferencePath: "file"}
	var row struct {
		BlockID       string `gorm:"column:block_id"`
		ReferencePath string `gorm:"column:reference_path"`
	}
	if err := svc.db.WithContext(ctx).Raw(`
		SELECT block_id, reference_path
		FROM content_block_attachment
		WHERE selector_kind = 'active' AND file_id = ?
		ORDER BY block_id, reference_path
		LIMIT 1
	`, fileID).Scan(&row).Error; err == nil && row.BlockID != "" {
		selector.BlockId = row.BlockID
		selector.ReferencePath = row.ReferencePath
	}
	response, err := svc.AuthorizeDownload(ctx, connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType:     openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST,
		EntityId:       postID,
		RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: selector},
	}))
	require.NoError(t, err)
	return response.Msg
}

func publicDownloadMemberContext(memberID string, identityID string, banned bool) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		Authenticated: true,
		Banned:        banned,
	})
}

func assertFileDownloadAccess(
	t *testing.T,
	response *openv1.AuthorizeDownloadResponse,
	availability openv1.FileDownloadAvailability,
	action openv1.FileDownloadAction,
	hasDownload bool,
) {
	t.Helper()
	assert.Equal(t, availability, response.GetAccess().GetAvailability())
	assert.Equal(t, action, response.GetAccess().GetAction())
	if hasDownload {
		require.NotNil(t, response.GetDownload())
		assert.NotEmpty(t, response.GetDownload().GetUrl())
		return
	}
	assert.Nil(t, response.GetDownload())
}

func assertDownloadToken(t *testing.T, signedURL string, fileID string, fileName string, expectedTTL time.Duration) {
	t.Helper()
	parsed, err := url.Parse(signedURL)
	require.NoError(t, err)
	tokenAndPath := strings.TrimPrefix(parsed.Path, "/media/")
	token, _, found := strings.Cut(tokenAndPath, "/")
	require.True(t, found)

	claims, err := mediaauth.ValidateToken(token, publicDownloadUnitSecret)
	require.NoError(t, err)
	assert.Equal(t, mediaauth.PurposeDownload, claims.Purpose)
	assert.Equal(t, mediaauth.ScopeExact, claims.ScopeType)
	assert.Equal(t, "media/"+fileID+".wav", claims.ScopeValue)
	assert.Equal(t, fileName, claims.Filename)
	assert.Equal(t, int64(expectedTTL/time.Second), claims.ExpiryUnix-claims.IssuedAtUnix)
}
