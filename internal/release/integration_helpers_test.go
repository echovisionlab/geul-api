//go:build integration

package release_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/releasetest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

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

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	return testutil.NewConcurrentPostIntegrationDB(t)
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	return testutil.IntegrationSpiceDB(t)
}

// releaseIntegrationAdminCtx is a request of identityID's onboarded Member,
// with the request actor every audited Release mutation records.
func releaseIntegrationAdminCtx(identityID string) context.Context {
	memberID := testutil.PostIntegrationMemberID(identityID)
	sessionID := testutil.IntegrationUUID()
	request, err := sharedtelemetry.NewPropagatedRequestContext(testutil.IntegrationUUID(), sharedtelemetry.MemberActor{
		IdentityID: identityID, MemberID: memberID, SessionID: sessionID,
	})
	if err != nil {
		panic(err)
	}
	return auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), request), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(sessionID),
		Authenticated: true,
		Onboarded:     true,
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

// newReleaseIntegrationUseCases returns the Release use cases with test Track
// files and audit.
func newReleaseIntegrationUseCases(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	audit domainaudit.Appender,
) *releasepkg.Service {
	t.Helper()
	return releasetest.NewService(t, releasetest.Deps{
		DB: db, SpiceDB: spiceDB, Audit: audit,
		Ports:        releasepostgres.Ports{ContentBlocks: newPageIntegrationContentBlockStore(t, spiceDB)},
		WaveformJobs: releaseadapter.NewWaveformJobs(db, releaseIntegrationTranscoderPublisher{}),
	})
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

type releaseFailingAuditAppender struct{}

func (releaseFailingAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("release audit unavailable")
}

func stringPtr(value string) *string { return &value }
