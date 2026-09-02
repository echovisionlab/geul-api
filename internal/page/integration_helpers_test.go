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
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func integrationTestUUID() string { return uuid.NewString() }

func integrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-page-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func grantIntegrationGlobalRole(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	identityID string,
	role policyv1.RoleID,
) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func seedExternalKratosIdentityWithTraits(
	t *testing.T,
	db *gorm.DB,
	identityID string,
	name string,
) string {
	t.Helper()
	email := identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: name,
	})
	memberID := integrationMemberID(identityID)
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
			memberID, identityID,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO account_identity (id, created_at)
			SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
			ON CONFLICT (id) DO NOTHING
		`, identityID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{
			ID: memberID, AccountIdentityID: &identityID, Nickname: name,
			Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
			SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
		}).Error
	}))
	return memberID
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

func workIntegrationAdminCtx(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(integrationMemberID(identityID)),
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

func newPageIntegrationFiles(db *gorm.DB, spiceDB *auth.SpiceDBClient) *filemedia.FileService {
	return filemedia.NewFileService(
		db,
		s3.New(s3.Options{Region: "us-east-1"}),
		noopAsyncPublisher{},
		"integration",
		"https://cdn.example.com",
		"https://media.example.com",
		"integration-secret",
		noopPageTranscoderPublisher{},
		spiceDB,
		filemedia.WithPagePolicyAccess(pageadapter.NewPolicyAccess(NewPolicyAuthority(spiceDB))),
	)
}

type fakeIdentityManager struct{ identity *auth.Identity }

func (f *fakeIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if f.identity == nil || f.identity.ID != identityID {
		return nil, errs.NotFound("identity", identityID)
	}
	return f.identity, nil
}

func (f *fakeIdentityManager) GetIdentityWithIncludeCredential(
	ctx context.Context,
	identityID string,
	_ string,
) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}

func postIntegrationIdentity(id string, preferredLocale string) *auth.Identity {
	traits := map[string]interface{}{"name": "Page Integration Admin"}
	if preferredLocale != "" {
		traits["preferred_locale"] = preferredLocale
	}
	return &auth.Identity{
		ID: id, ExternalID: integrationMemberID(id), State: auth.KratosStateActive, Traits: traits,
	}
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
