//go:build integration

package page

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"slices"
	"testing"

	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Page integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Page integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// newIntegrationPageService returns the Page use cases over deps, filling
// the collaborators a test leaves unset with no-op stand-ins and the durable
// audit writer of deps.DB. checker, when given, decides Page actions in
// place of SpiceDB.
func newIntegrationPageService(t *testing.T, deps Deps, checker ...CollaborationPermissionChecker) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = integrationSpiceDB(t)
	}
	if deps.Runtime == nil {
		deps.Runtime = newPageRuntimeForTest(deps.DB, "https://cdn.example.com")
	}
	if deps.Files == nil {
		deps.Files = &recordingPageDeleteFileDeleter{}
	}
	if deps.Signals == nil {
		deps.Signals = noopAsyncPublisher{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = newPageIntegrationContentBlockStore(t, deps.SpiceDB)
	}
	if deps.MediaHydrator == nil {
		deps.MediaHydrator = passthroughPageContentBlockMediaHydrator{}
	}
	if deps.MenuTargets == nil {
		deps.MenuTargets = noopPageMenuTargets{}
	}
	if deps.Members == nil {
		deps.Members = memberadapter.PageMembers{}
	}
	if deps.ShareLinks == nil {
		deps.ShareLinks = sharelinkadapter.PageLinks{}
	}
	if deps.Audit == nil {
		deps.Audit = apitelemetry.NewDurableWriter(deps.DB)
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	if len(checker) != 0 {
		service.permissions = checker[0]
	}
	return service
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func requireSynchronousAuthorizedResource(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	lookup policyv1.ResourceLookup,
	resourceID string,
	identityID string,
	expected bool,
) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	resources, err := spiceDB.LookupResources(t.Context(), lookup, actor)
	require.NoError(t, err)
	require.Equal(t, expected, slices.Contains(resources, resourceID))
}

func seedImageBindingUploadedFileFixture(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(key))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`,
		id, id, digest[:],
	).Error)
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &id,
		Kind:         "image",
		Extension:    "webp",
		MimeType:     "image/webp",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	assetDigest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: 1024, Sha256: assetDigest[:],
	})
	require.NoError(t, err)
	return id
}

func requireFileRowExists(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

// workIntegrationAdminCtx is a member request, so mutations can name their
// audit records.
func workIntegrationAdminCtx(identityID string) context.Context {
	request, err := sharedtelemetry.NewPublicRequestContext("192.0.2.40")
	if err != nil {
		panic(err)
	}
	return auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), request), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(testutil.PostIntegrationMemberID(identityID)),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})
}

type pageTestRenderConfig struct{}

type noopPageMenuTargets struct{}

func (noopPageMenuTargets) UpdateSlug(
	context.Context,
	*gorm.DB,
	string,
	string,
	string,
	string,
) error {
	return nil
}

func (noopPageMenuTargets) Remove(context.Context, *gorm.DB, string, string, string) error {
	return nil
}

func (pageTestRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func newPageRuntimeForTest(db *gorm.DB, cdnDomain string) *pageadapter.Runtime {
	planner := og.NewPlanner(db, cdnDomain, pageTestRenderConfig{}, pageadapter.NewProjection())
	return pageadapter.NewRuntime(
		cdnDomain,
		og.NewRefresher(planner, og.NewResolver(pageadapter.NewRequests())),
	)
}

type noopAsyncPublisher struct{}

func (noopAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (noopAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

type noopPageTranscoderPublisher struct{}

func (noopPageTranscoderPublisher) PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error {
	return nil
}

func (noopPageTranscoderPublisher) PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error {
	return nil
}

func (noopPageTranscoderPublisher) PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error {
	return nil
}

func newPageIntegrationFiles(db *gorm.DB, spiceDB *auth.SpiceDBClient) *filemedia.Service {
	files, err := filemedia.NewService(filemedia.Deps{
		DB:                     db,
		S3:                     s3.New(s3.Options{Region: "us-east-1"}),
		AsyncPublisher:         noopAsyncPublisher{},
		S3Bucket:               "integration",
		CDNDomain:              "https://cdn.example.com",
		MediaDomain:            "https://media.example.com",
		MediaSecret:            "integration-secret",
		Transcoder:             noopPageTranscoderPublisher{},
		SpiceDB:                spiceDB,
		Audit:                  &audittest.Recorder{},
		PagePolicyAccess:       pageadapter.NewPolicyAccess(NewPolicyAuthority(spiceDB)),
		LegalRoutes:            pageUnusedLegalRoutes{},
		PostAccess:             pageUnusedPostAccess{},
		WorkAttachment:         pageUnusedWorkAttachment{},
		WorkPolicyAccess:       pageUnusedWorkPolicyAccess{},
		ProgramEventAttachment: pageUnusedProgramEventAttachment{},
		TrackAttachment:        pageUnusedTrackAttachment{},
		ReleasePolicyAccess:    pageUnusedReleasePolicyAccess{},
		AudienceAccess:         pageUnusedAudienceAccess{},
		MemberSummaries:        pageUnusedMemberSummaries{},
	})
	if err != nil {
		panic(err)
	}
	return files
}

type recordingArtistFileDeleter struct{ deletedIDs []string }

func (d *recordingArtistFileDeleter) DeleteFileByID(_ context.Context, fileID string) error {
	d.deletedIDs = append(d.deletedIDs, fileID)
	return nil
}

type recordingPageDeleteFileDeleter struct{ recordingArtistFileDeleter }

func (*recordingPageDeleteFileDeleter) ResolveAuthorizedPageFeaturedImage(
	context.Context,
	string,
	string,
) (*commonv1.MediaDelivery, error) {
	return nil, nil
}

func attachInternalResourcePolicy(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	resourceID string,
) {
	t.Helper()
	mutation, err := policyv1.Page.TouchPolicy(resourceID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func upsertTypedBlockTranslationEntryMetadata(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	locale string,
	input translation.EntryWrite,
) error {
	if entityType != "page" {
		return errs.InvalidEntityType(entityType)
	}
	return UpsertTranslationMetadataEntry(ctx, db, entityID, locale, input)
}

func loadCurrentBlockDocumentRevision(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) (uuid.UUID, error) {
	if entityType != "page" {
		return uuid.Nil, errs.InvalidEntityType(entityType)
	}
	var revisionValue string
	result := db.WithContext(ctx).Raw(`
		SELECT document.revision
		FROM page
		JOIN content_document AS document ON document.id = page.content_document_id
		WHERE page.id = ?
	`, entityID).Scan(&revisionValue)
	if result.Error != nil {
		return uuid.Nil, result.Error
	}
	if result.RowsAffected != 1 || revisionValue == "" {
		return uuid.Nil, errs.FailedPrecondition("Page Content Document is not initialized")
	}
	revision, err := uuid.Parse(revisionValue)
	if err != nil || revision == uuid.Nil {
		return uuid.Nil, errs.FailedPrecondition("Page Content Document revision is invalid")
	}
	return revision, nil
}

type pageFileReuseAuthorizer struct{ spiceDB *auth.SpiceDBClient }

func NewContentBlockFileReuseAuthorizer(spiceDB *auth.SpiceDBClient) contentblock.FileReuseAuthorizer {
	return &pageFileReuseAuthorizer{spiceDB: spiceDB}
}

func (a *pageFileReuseAuthorizer) AuthorizeFileReuse(
	ctx context.Context,
	tx *gorm.DB,
	document contentblock.Document,
	_ contentblock.FullBlock,
	_ contentblock.FileReference,
	file contentblock.File,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.MemberID == "" {
		return errs.AuthenticationRequired()
	}
	if principal.Banned {
		return errs.AccountBanned()
	}
	var alreadyInDocument bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1 FROM content_block_attachment AS attachment
			JOIN content_block AS block ON block.id = attachment.block_id
			WHERE attachment.selector_kind = 'active'
				AND attachment.file_id = ? AND block.document_id = ?
		)`, file.ID, document.ID).Scan(&alreadyInDocument).Error; err != nil {
		return err
	}
	if alreadyInDocument {
		return nil
	}
	var uploader struct {
		MemberID *string `gorm:"column:uploaded_by_member_id"`
	}
	if err := tx.WithContext(ctx).Table("file").Select("uploaded_by_member_id").
		Where("id = ?", file.ID).Scan(&uploader).Error; err != nil {
		return err
	}
	if uploader.MemberID != nil && *uploader.MemberID == principal.MemberID.String() {
		return nil
	}
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return err
	}
	return authz.RequirePlatformPermission(ctx, a.spiceDB, can)
}

func (publisher noopAsyncPublisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}

// The ports this test does not reach panic if called.
type pageUnusedLegalRoutes struct{ filemedia.LegalRouteIdentity }
type pageUnusedPostAccess struct{ filemedia.PostAccess }
type pageUnusedWorkAttachment struct{ filemedia.WorkAttachment }
type pageUnusedWorkPolicyAccess struct{ filemedia.WorkPolicyAccess }
type pageUnusedProgramEventAttachment struct {
	filemedia.ProgramEventAttachment
}
type pageUnusedTrackAttachment struct{ filemedia.TrackAttachment }
type pageUnusedReleasePolicyAccess struct{ filemedia.ReleasePolicyAccess }
type pageUnusedAudienceAccess struct{ filemedia.AudienceAccess }
type pageUnusedMemberSummaries struct{ filemedia.MemberSummaries }
