//go:build integration

package public

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/testutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type relationSourceBlockingLogger struct {
	logger.Interface
	loaded  chan struct{}
	release chan struct{}
	once    sync.Once
	count   atomic.Int32
	trigger int32
}

func (l *relationSourceBlockingLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	sql, rows := fc()
	if strings.Contains(sql, "content_block_attachment") && strings.Contains(sql, "download_audience") &&
		strings.Contains(sql, "JOIN file") && l.count.Add(1) == l.trigger {
		l.once.Do(func() {
			close(l.loaded)
			<-l.release
		})
	}
	l.Interface.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
}

func TestAuthorizeDownloadRejectsStalePhase1AfterReplacementIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	spiceDB := publicIntegrationSpiceDBClient(t)
	postID, documentID, blockID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	originalFileID, replacementFileID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, content_document_id) VALUES (?::uuid, ?, ?::uuid)`, postID, managev1.PostStatus_POST_STATUS_PUBLISHED.String(), documentID).Error)
	for _, fileID := range []string{originalFileID, replacementFileID} {
		require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256) VALUES (?::uuid, ?, 'wav', 'audio/wav', 4096, ?)`, fileID, fileID, make([]byte, 32)).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES (?::uuid, 'file', 'active', ?::uuid, 'public')`, blockID, originalFileID).Error)

	request := connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType: openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST,
		EntityId:   postID,
		RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{
			BlockId: blockID, ReferencePath: "file",
		}},
	})
	initialService := NewFileService(db, spiceDB, "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	initial, err := initialService.AuthorizeDownload(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD, initial.Msg.GetAccess().GetAction())
	initialURL := initial.Msg.GetDownload().GetUrl()

	loaded, release := make(chan struct{}), make(chan struct{})
	serviceDB := db.Session(&gorm.Session{Logger: &relationSourceBlockingLogger{
		Interface: db.Logger,
		loaded:    loaded,
		release:   release,
		trigger:   1,
	}})
	svc := NewFileService(serviceDB, spiceDB, "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	type authorizationResult struct {
		response *connect.Response[openv1.AuthorizeDownloadResponse]
		err      error
	}
	authorized := make(chan authorizationResult, 1)
	go func() {
		response, err := svc.AuthorizeDownload(t.Context(), request)
		authorized <- authorizationResult{response: response, err: err}
	}()
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("authorization did not reach the phase-one source snapshot")
	}

	replaced := make(chan error, 1)
	replacementStarted := make(chan struct{})
	go func() {
		close(replacementStarted)
		replaced <- db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`DELETE FROM content_block_attachment_download_audience_segment WHERE block_id = ?::uuid AND reference_path = 'file'`, blockID).Error; err != nil {
				return err
			}
			return tx.Exec(`UPDATE content_block_attachment SET file_id = ?::uuid, download_audience = ? WHERE block_id = ?::uuid AND reference_path = 'file'`, replacementFileID, string(mediaasset.FileDownloadAudienceDisabled), blockID).Error
		})
	}()
	<-replacementStarted
	select {
	case err := <-replaced:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not commit while authorization was paused after phase one")
	}
	close(release)
	result := <-authorized
	require.NoError(t, result.err)
	require.NotNil(t, result.response)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, result.response.Msg.GetAccess().GetAction())
	require.Nil(t, result.response.Msg.GetDownload())
	assertDownloadToken(t, initialURL, originalFileID, originalFileID+".wav", mediaauth.DownloadTTL)
}

func TestAuthorizeDownloadHoldsPhaseTwoRelationThroughSigningIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	spiceDB := publicIntegrationSpiceDBClient(t)
	postID, documentID, blockID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	originalFileID, replacementFileID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, content_document_id) VALUES (?::uuid, ?, ?::uuid)`, postID, managev1.PostStatus_POST_STATUS_PUBLISHED.String(), documentID).Error)
	for _, fileID := range []string{originalFileID, replacementFileID} {
		require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256) VALUES (?::uuid, ?, 'wav', 'audio/wav', 4096, ?)`, fileID, fileID, make([]byte, 32)).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES (?::uuid, 'file', 'active', ?::uuid, 'public')`, blockID, originalFileID).Error)

	loaded, release := make(chan struct{}), make(chan struct{})
	serviceDB := db.Session(&gorm.Session{Logger: &relationSourceBlockingLogger{
		Interface: db.Logger, loaded: loaded, release: release, trigger: 2,
	}})
	svc := NewFileService(serviceDB, spiceDB, "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	request := connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType: openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST,
		EntityId:   postID,
		RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{
			BlockId: blockID, ReferencePath: "file",
		}},
	})
	type authorizationResult struct {
		response *connect.Response[openv1.AuthorizeDownloadResponse]
		err      error
	}
	authorized := make(chan authorizationResult, 1)
	go func() {
		response, err := svc.AuthorizeDownload(t.Context(), request)
		authorized <- authorizationResult{response: response, err: err}
	}()
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("authorization did not reach the phase-two locked source recheck")
	}
	replaced := make(chan error, 1)
	replacementStarted := make(chan struct{})
	go func() {
		close(replacementStarted)
		replaced <- db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`DELETE FROM content_block_attachment_download_audience_segment WHERE block_id = ?::uuid AND reference_path = 'file'`, blockID).Error; err != nil {
				return err
			}
			return tx.Exec(`UPDATE content_block_attachment SET file_id = ?::uuid, download_audience = ? WHERE block_id = ?::uuid AND reference_path = 'file'`, replacementFileID, string(mediaasset.FileDownloadAudienceDisabled), blockID).Error
		})
	}()
	<-replacementStarted
	select {
	case err := <-replaced:
		require.NoError(t, err)
		t.Fatal("replacement committed while phase two still held the exact relation lock")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	result := <-authorized
	require.NoError(t, result.err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD, result.response.Msg.GetAccess().GetAction())
	assertDownloadToken(t, result.response.Msg.GetDownload().GetUrl(), originalFileID, originalFileID+".wav", mediaauth.DownloadTTL)
	require.NoError(t, <-replaced)

	afterReplacement, err := svc.AuthorizeDownload(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, afterReplacement.Msg.GetAccess().GetAction())
}

func TestHydrateContentBlockMediaDoesNotAttachSharedFileDeliveryToReplacedRelationIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	spiceDB := publicIntegrationSpiceDBClient(t)
	postID, documentID := uuid.NewString(), uuid.NewString()
	stableBlockID, replacedBlockID := uuid.NewString(), uuid.NewString()
	originalFileID, replacementFileID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, content_document_id) VALUES (?::uuid, ?, ?::uuid)`, postID, managev1.PostStatus_POST_STATUS_PUBLISHED.String(), documentID).Error)
	for _, fileID := range []string{originalFileID, replacementFileID} {
		require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256) VALUES (?::uuid, ?, 'png', 'image/png', 4096, ?)`, fileID, fileID, make([]byte, 32)).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES
		(?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb),
		(?::uuid, ?::uuid, NULL, 'root', 1, 'file', '{}'::jsonb)`, stableBlockID, documentID, replacedBlockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES
		(?::uuid, 'file', 'active', ?::uuid, 'public'),
		(?::uuid, 'file', 'active', ?::uuid, 'public')`, stableBlockID, originalFileID, replacedBlockID, originalFileID).Error)

	items, err := filemedia.LoadContentBlockMediaReferences(t.Context(), db, uuid.MustParse(documentID))
	require.NoError(t, err)
	require.Len(t, items, 2)
	loaded, release := make(chan struct{}), make(chan struct{})
	serviceDB := db.Session(&gorm.Session{Logger: &relationSourceBlockingLogger{
		Interface: db.Logger, loaded: loaded, release: release, trigger: 1,
	}})
	svc := NewFileService(serviceDB, spiceDB, "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	hydrationContext := mediaasset.WithContentDownloadOwnerAuthorization(t.Context(), mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "post", ResourceID: postID, Status: managev1.PostStatus_POST_STATUS_PUBLISHED.String(), DocumentID: documentID,
		Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	})
	type hydrationResult struct {
		items []*contentv1.ContentBlockMediaItem
		err   error
	}
	hydrated := make(chan hydrationResult, 1)
	go func() {
		result, hydrateErr := svc.HydrateAuthorizedContentBlockMedia(hydrationContext, items)
		hydrated <- hydrationResult{items: result, err: hydrateErr}
	}()
	select {
	case <-loaded:
	case result := <-hydrated:
		require.NoError(t, result.err)
		t.Fatal("hydration completed before the phase-one relation snapshot barrier")
	case <-time.After(5 * time.Second):
		t.Fatal("hydration did not reach the phase-one relation snapshot")
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM content_block_attachment_download_audience_segment WHERE block_id = ?::uuid AND reference_path = 'file'`, replacedBlockID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE content_block_attachment SET file_id = ?::uuid, download_audience = 'disabled' WHERE block_id = ?::uuid AND reference_path = 'file'`, replacementFileID, replacedBlockID).Error
	}))
	close(release)
	result := <-hydrated
	require.NoError(t, result.err)
	require.Len(t, result.items, 2)

	byBlock := make(map[string]*contentv1.ContentBlockMediaItem, len(result.items))
	for _, item := range result.items {
		byBlock[item.GetSelector().GetBlockId()] = item
	}
	stable := byBlock[stableBlockID]
	require.NotNil(t, stable.GetDelivery())
	require.NotNil(t, stable.GetDelivery().GetInline())
	require.NotNil(t, stable.GetDelivery().GetDownload())
	require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD, stable.GetDownloadAction())

	replaced := byBlock[replacedBlockID]
	require.Nil(t, replaced.GetDelivery())
	require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_NONE, replaced.GetDownloadAction())
}

func TestAuthorizeDownloadRequiresCurrentActivePrincipalForAuthenticatedPolicyIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	postID, _, blockID, fileID := seedDownloadRelationForPrincipalFenceIntegration(
		t, db, managev1.PostStatus_POST_STATUS_PUBLISHED.String(), mediaasset.FileDownloadAudienceAuthenticated,
	)
	member := stack.CreateUser(t, policyv1.Role.User().ID())
	ctx := auth.WithUser(t.Context(), member.AuthUserInfo())
	svc := NewFileService(db, stack.SpiceDBClient, "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	request := connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType: openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST,
		EntityId:   postID,
		RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{
			BlockId: blockID, ReferencePath: "file",
		}},
	})

	active, err := svc.AuthorizeDownload(ctx, request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD, active.Msg.GetAccess().GetAction())

	assertDenied := func() {
		t.Helper()
		sources, loadErr := mediaasset.LoadContentBlockDownloadSources(t.Context(), db, []mediaasset.ContentBlockDownloadSelector{{BlockID: blockID, ReferencePath: "file"}})
		require.NoError(t, loadErr)
		allowed, evaluateErr := mediaasset.EvaluateFileDownloadAccessBatch(t.Context(), db, stack.SpiceDBClient, sources, member.AuthUserInfo())
		require.NoError(t, evaluateErr)
		require.False(t, allowed[mediaasset.ContentBlockDownloadPolicyKey(blockID, "file")])
		response, authorizeErr := svc.AuthorizeDownload(ctx, request)
		require.NoError(t, authorizeErr)
		require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, response.Msg.GetAccess().GetAction())
		require.Nil(t, response.Msg.GetDownload())
	}
	require.NoError(t, db.Exec(`UPDATE member SET onboarded = FALSE WHERE id = ?::uuid`, member.MemberID).Error)
	assertDenied()
	require.NoError(t, db.Exec(`UPDATE member SET onboarded = TRUE WHERE id = ?::uuid`, member.MemberID).Error)
	require.NoError(t, db.Exec(`UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`, member.IdentityID).Error)
	assertDenied()
	require.NoError(t, db.Exec(`UPDATE kratos.identities SET state = 'active', metadata_admin = '{"banned":true}'::jsonb WHERE id = ?::uuid`, member.IdentityID).Error)
	assertDenied()
	require.NoError(t, db.Exec(`UPDATE kratos.identities SET metadata_admin = '{"banned":false}'::jsonb WHERE id = ?::uuid`, member.IdentityID).Error)
	require.NoError(t, db.Exec(`UPDATE member SET account_identity_id = NULL, deleted_at = CURRENT_TIMESTAMP WHERE id = ?::uuid`, member.MemberID).Error)
	assertDenied()

	var currentFileID string
	require.NoError(t, db.Raw(`SELECT file_id::text FROM content_block_attachment WHERE block_id = ?::uuid AND reference_path = 'file'`, blockID).Scan(&currentFileID).Error)
	require.Equal(t, fileID, currentFileID)
}

func TestAuthorizeDownloadRestrictedAllMembersRequiresCurrentActiveMemberIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	postID, _, blockID, _ := seedDownloadRelationForPrincipalFenceIntegration(
		t, db, managev1.PostStatus_POST_STATUS_PUBLISHED.String(), mediaasset.FileDownloadAudienceRestricted,
	)
	segmentID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO audience_segment (id, name, segment_type, created_at) VALUES (?::uuid, 'All members download', ?, CURRENT_TIMESTAMP)`, segmentID, managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS.String()).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment_download_audience_segment (block_id, reference_path, audience_segment_id) VALUES (?::uuid, 'file', ?::uuid)`, blockID, segmentID).Error)
	member := stack.CreateUser(t, policyv1.Role.User().ID())
	ctx := auth.WithUser(t.Context(), member.AuthUserInfo())
	svc := NewFileService(
		db, stack.SpiceDBClient, "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL,
		WithDownloadSegmentConfigs(mediaassetadapter.NewSegmentConfigs()),
	)
	request := connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType: openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST,
		EntityId:   postID,
		RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{
			BlockId: blockID, ReferencePath: "file",
		}},
	})

	active, err := svc.AuthorizeDownload(ctx, request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD, active.Msg.GetAccess().GetAction())
	require.NotNil(t, active.Msg.GetDownload())

	anonymous, err := svc.AuthorizeDownload(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_SIGN_IN, anonymous.Msg.GetAccess().GetAction())
	require.Nil(t, anonymous.Msg.GetDownload())

	require.NoError(t, db.Exec(`UPDATE member SET onboarded = FALSE WHERE id = ?::uuid`, member.MemberID).Error)
	inactive, err := svc.AuthorizeDownload(ctx, request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, inactive.Msg.GetAccess().GetAction())
	require.Nil(t, inactive.Msg.GetDownload())

	require.NoError(t, db.Exec(`UPDATE member SET onboarded = TRUE WHERE id = ?::uuid`, member.MemberID).Error)
	require.NoError(t, db.Exec(`UPDATE audience_segment SET archived_at = CURRENT_TIMESTAMP WHERE id = ?::uuid`, segmentID).Error)
	archived, err := svc.AuthorizeDownload(ctx, request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, archived.Msg.GetAccess().GetAction())
	require.Nil(t, archived.Msg.GetDownload())

	require.NoError(t, db.Exec(`DELETE FROM content_block_attachment_download_audience_segment WHERE block_id = ?::uuid AND reference_path = 'file'`, blockID).Error)
	restrictedEmpty, err := svc.AuthorizeDownload(ctx, request)
	require.NoError(t, err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, restrictedEmpty.Msg.GetAccess().GetAction())
	require.Nil(t, restrictedEmpty.Msg.GetDownload())
}

func TestAuthorizeDownloadDraftPublicPolicyRejectsPrincipalRevokedAfterPhaseOneIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	postID, _, blockID, _ := seedDownloadRelationForPrincipalFenceIntegration(
		t, db, managev1.PostStatus_POST_STATUS_DRAFT.String(), mediaasset.FileDownloadAudiencePublic,
	)
	member := stack.CreateUser(t, policyv1.Role.Admin().ID())
	testutil.GrantPostIntegrationRole(t, stack.SpiceDBClient, member.IdentityID, policyv1.Role.Admin())
	ctx := auth.WithUser(t.Context(), member.AuthUserInfo())

	loaded, release := make(chan struct{}), make(chan struct{})
	serviceDB := db.Session(&gorm.Session{Logger: &relationSourceBlockingLogger{
		Interface: db.Logger, loaded: loaded, release: release, trigger: 1,
	}})
	svc := NewFileService(serviceDB, stack.SpiceDBClient, "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	request := connect.NewRequest(&openv1.AuthorizeDownloadRequest{
		EntityType: openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST,
		EntityId:   postID,
		RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{
			BlockId: blockID, ReferencePath: "file",
		}},
	})
	type authorizationResult struct {
		response *connect.Response[openv1.AuthorizeDownloadResponse]
		err      error
	}
	authorized := make(chan authorizationResult, 1)
	go func() {
		response, err := svc.AuthorizeDownload(ctx, request)
		authorized <- authorizationResult{response: response, err: err}
	}()
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("draft authorization did not reach phase one")
	}
	require.NoError(t, db.Exec(`UPDATE member SET account_identity_id = NULL, deleted_at = CURRENT_TIMESTAMP WHERE id = ?::uuid`, member.MemberID).Error)
	close(release)
	result := <-authorized
	require.NoError(t, result.err)
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, result.response.Msg.GetAccess().GetAction())
	require.Nil(t, result.response.Msg.GetDownload())
}

func seedDownloadRelationForPrincipalFenceIntegration(
	t *testing.T,
	db *gorm.DB,
	status string,
	audience mediaasset.FileDownloadAudience,
) (string, string, string, string) {
	t.Helper()
	postID, documentID, blockID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, content_document_id) VALUES (?::uuid, ?, ?::uuid)`, postID, status, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256) VALUES (?::uuid, 'principal-fence', 'wav', 'audio/wav', 4096, ?)`, fileID, make([]byte, 32)).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES (?::uuid, 'file', 'active', ?::uuid, ?)`, blockID, fileID, string(audience)).Error)
	return postID, documentID, blockID, fileID
}
