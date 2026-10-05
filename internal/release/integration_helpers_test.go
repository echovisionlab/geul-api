//go:build integration

package release_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

const maxReleaseCascadeAuthorizationTracks = 999

var errNilTransactionalExecutor = errors.New("transactional fixture executor is required")

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Release integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "cleanup Release integration runtime: %v\n", err)
		code = 1
	}
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Release integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	return testutil.NewIntegrationDB(t)
}

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	return testutil.NewConcurrentPostIntegrationDB(t)
}

func integrationTestUUID() string { return testutil.IntegrationUUID() }

func integrationMemberID(identityID string) string {
	return testutil.PostIntegrationMemberID(identityID)
}

func seedExternalKratosIdentityWithTraits(t *testing.T, db *gorm.DB, identityID, name string) string {
	return testutil.SeedPostIntegrationIdentity(t, db, identityID, name)
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	return testutil.IntegrationSpiceDB(t)
}

func grantIntegrationGlobalRole(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	identityID string,
	role policyv1.RoleID,
) {
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, role)
}

func releaseIntegrationAdminCtx(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(integrationMemberID(identityID)),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
	})
}

func withAuditedRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	return testutil.NewAuditContext(t, user.IdentityID.String(), user.MemberID.String())
}

func newPageIntegrationContentBlockStore(t *testing.T, spiceDB *auth.SpiceDBClient) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	return store
}

func creativeContentIntegrationDocument(sourceLocale, text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		SourceLocale:            sourceLocale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: sourceLocale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
					Text: &contentv1.RichTextStyledText{Text: text},
				}}},
			}},
		}}}},
	}
}

type releaseIntegrationTranscoderPublisher struct{}

func (releaseIntegrationTranscoderPublisher) PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error {
	return nil
}

func (releaseIntegrationTranscoderPublisher) PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error {
	return nil
}

func (releaseIntegrationTranscoderPublisher) PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error {
	return nil
}

type noopAsyncPublisher struct{}

func (noopAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (noopAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (noopAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	_ string,
	_ string,
	_ proto.Message,
) error {
	if executor == nil {
		return errNilTransactionalExecutor
	}
	return nil
}

type recordingArtistFileDeleter struct{ deletedIDs []string }

func (d *recordingArtistFileDeleter) DeleteFileByID(_ context.Context, fileID string) error {
	d.deletedIDs = append(d.deletedIDs, fileID)
	return nil
}

func (*recordingArtistFileDeleter) CleanupTrackUploadSessions(context.Context, string, string) error {
	return nil
}

func (*recordingArtistFileDeleter) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

type recordingTrackFileDeleter struct{ deleted []string }

func (d *recordingTrackFileDeleter) DeleteFileByID(_ context.Context, fileID string) error {
	d.deleted = append(d.deleted, fileID)
	return nil
}

func (*recordingTrackFileDeleter) CleanupTrackUploadSessions(context.Context, string, string) error {
	return nil
}

func (*recordingTrackFileDeleter) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

func newReleaseIntegrationService(
	t *testing.T,
	db *gorm.DB,
	adminID string,
	files releasepkg.TrackFileManager,
) *releasepkg.ReleaseService {
	t.Helper()
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	return releasepkg.NewReleaseService(
		db,
		spiceDB,
		testutil.SetupOryStack(t).KratosClient,
		releaseadapter.NewTrackFiles(files),
		releaseadapter.NewAssets(""),
		releaseadapter.NewOG(""),
		releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
		noopAsyncPublisher{},
		releasepkg.WithReleaseContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
	)
}

func newReferenceCategoryService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *referencecatalog.CategoryService {
	return referencecatalog.NewCategoryService(
		db,
		referencecatalogmenuadapter.NewTargets(nil),
		spiceDB,
	)
}

func newReferenceGenreService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *referencecatalog.GenreService {
	return referencecatalog.NewGenreService(db, spiceDB)
}

func newReferenceStyleService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *referencecatalog.StyleService {
	return referencecatalog.NewStyleService(db, spiceDB)
}

func referenceShortSuffix() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

func requireRelationWhereCount(t *testing.T, db *gorm.DB, tableName, where string, want int64, args ...any) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM `+tableName+` WHERE `+where, args...).Scan(&count).Error)
	require.Equal(t, want, count)
}

func requireNoMapping(t *testing.T, db *gorm.DB, tableName, columnName, value string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM `+tableName+` WHERE `+columnName+` = ?`, value).Scan(&count).Error)
	require.Zero(t, count)
}

func seedFileDeleteLifecycleFile(t *testing.T, db *gorm.DB, fileID, fileName, mimeType, extension string) {
	t.Helper()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: extension, SHA256: digest[:], CreatedAt: time.Now().UTC(),
	}).Error)
}

func seedIntegrationFile(t *testing.T, db *gorm.DB, fileID, fileName, mimeType string, attemptID *string) {
	t.Helper()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: model.GetExtensionFromMime(mimeType), SHA256: digest[:], IngestAttemptID: attemptID,
	}).Error)
}

func seedHardCutReadyPublicAsset(
	t *testing.T,
	db *gorm.DB,
	kind, extension, mimeType string,
	sourceFileID *string,
) *commonv1.AssetRef {
	t.Helper()
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: sourceFileID,
		Kind:         kind,
		Extension:    extension,
		MimeType:     mimeType,
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: 1024, Sha256: digest[:],
	})
	require.NoError(t, err)
	ref, err := lifecycle.ReadyAssetRef(t.Context(), asset.ID)
	require.NoError(t, err)
	return ref
}

func requireHardCutPublicAssetStatus(t *testing.T, db *gorm.DB, assetID, status string) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Equal(t, status, asset.Status)
}

func requireFileRowExists(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.File{}).Where("id = ?", fileID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func requireNoRow(t *testing.T, db *gorm.DB, tableName, id string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(tableName).Where("id = ?", id).Count(&count).Error)
	require.Zero(t, count)
}

func requireNoShareLinks(t *testing.T, db *gorm.DB, entityType, entityID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM share_link WHERE entity_type = ? AND entity_id = ?`,
		entityType,
		entityID,
	).Scan(&count).Error)
	require.Zero(t, count)
}

func requireNoReleaseTranslationRows(t *testing.T, db *gorm.DB, releaseID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM release_translation WHERE entity_id = ?`, releaseID).Scan(&count).Error)
	require.Zero(t, count)
}

type releaseFailingAuditAppender struct{}

func (releaseFailingAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("release audit unavailable")
}

func stringPtr(value string) *string { return &value }
