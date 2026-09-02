//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workogadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var workIntegrationSuite *testutil.OryIntegrationSuite

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Work integration suite: %v\n", err)
		os.Exit(1)
	}
	workIntegrationSuite = suite
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Work integration suite: %v\n", err)
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

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	db, err := gorm.Open(gormpostgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	opened, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	return db
}

func integrationTestUUID() string { return uuid.NewString() }

func ptrString(value string) *string { return &value }

func integrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-work-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func grantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func seedExternalKratosIdentityWithTraits(t *testing.T, db *gorm.DB, identityID, name string) string {
	t.Helper()
	email := identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: name,
	})
	memberID := integrationMemberID(identityID)
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error; err != nil {
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

func insertWorkIntegrationSession(t *testing.T, db *gorm.DB, identityID string) string {
	t.Helper()
	sessionID := integrationTestUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (
			id, identity_id, active, authenticated_at, expires_at,
			created_at, updated_at, nid, authentication_methods
		)
		SELECT ?::uuid, id, TRUE, NOW(), NOW() + INTERVAL '1 hour',
		       NOW(), NOW(), nid, '[]'::jsonb
		FROM kratos.identities
		WHERE id = ?::uuid
	`, sessionID, identityID).Error)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM kratos.sessions WHERE id = ?::uuid", sessionID).Error })
	return sessionID
}

type workRenderConfig struct{}

func (workRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	digest := sha256.Sum256(payload)
	return payload, fmt.Sprintf("%x", digest[:]), nil
}

func newWorkRuntimeForTest(db *gorm.DB, cdnDomain string) *workogadapter.Runtime {
	planner := og.NewPlanner(db, cdnDomain, workRenderConfig{}, workogadapter.NewProjection())
	return workogadapter.NewRuntime(
		db,
		cdnDomain,
		og.NewRefresher(planner, og.NewResolver(workogadapter.NewRequests())),
	)
}

func newContentBlockFileReuseAuthorizer(_ *auth.SpiceDBClient) contentblock.FileReuseAuthorizer {
	return allowWorkFileReuse{}
}

type allowWorkFileReuse struct{}

func (allowWorkFileReuse) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

type fakeIdentityManager struct{ identity *auth.Identity }

func (f *fakeIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if f.identity == nil || f.identity.ID != identityID {
		return nil, fmt.Errorf("identity not found")
	}
	return f.identity, nil
}
func (f *fakeIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}
func (*fakeIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (f *fakeIdentityManager) UpdateIdentityTraits(_ context.Context, identityID string, traits structured.Fields) error {
	if f.identity == nil || f.identity.ID != identityID {
		return fmt.Errorf("identity not found")
	}
	if f.identity.Traits == nil {
		f.identity.Traits = structured.Fields{}
	}
	for key, value := range traits {
		f.identity.Traits[key] = value
	}
	return nil
}
func (*fakeIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (*fakeIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (*fakeIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }
func (*fakeIdentityManager) DeleteIdentitySessions(context.Context, string) error   { return nil }
func (*fakeIdentityManager) DeleteIdentity(context.Context, string) error           { return nil }
func (f *fakeIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if f.identity == nil {
		return "", nil
	}
	return f.identity.CurrentEmail(), nil
}

func postIntegrationIdentity(id, locale string) *auth.Identity {
	return &auth.Identity{
		ID: id, State: auth.KratosStateActive,
		Traits: structured.Fields{"name": "Work Admin", "preferred_locale": locale},
	}
}

type noopAsyncPublisher struct{}

func (noopAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (noopAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error { return nil }

func seedIntegrationFile(t *testing.T, db *gorm.DB, fileID, fileName, mimeType string, attemptID *string) {
	t.Helper()
	digest := sha256.Sum256([]byte(fileID))
	extension := model.GetExtensionFromMime(mimeType)
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: extension, SHA256: digest[:], IngestAttemptID: attemptID,
	}).Error)
	if len(mimeType) >= 6 && mimeType[:6] == "image/" {
		lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
		asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
			SourceFileID: &fileID, Kind: "image", Extension: extension, MimeType: mimeType,
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		})
		require.NoError(t, err)
		assetDigest := sha256.Sum256([]byte(asset.ID))
		_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
			AssetId: asset.ID, FileSize: 1024, Sha256: assetDigest[:],
		})
		require.NoError(t, err)
	}
}

func requireFileRowExists(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func requireSynchronousAuthorizedResource(t *testing.T, spiceDB *auth.SpiceDBClient, lookup policyv1.ResourceLookup, resourceID, identityID string, expected bool) {
	t.Helper()
	resource, err := policyv1.Work.Resource(resourceID)
	require.NoError(t, err)
	require.Equal(t, resource.Type(), lookup.ResourceType())
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	resources, err := spiceDB.LookupResources(t.Context(), lookup, actor)
	require.NoError(t, err)
	require.Equal(t, expected, slices.Contains(resources, resource.ID()))
}

func mapPlaceServiceForTest(t *testing.T, db *gorm.DB, cdnDomain string, spiceDB *auth.SpiceDBClient) *referencecatalog.MapPlaceService {
	t.Helper()
	return referencecatalog.NewMapPlaceService(
		db,
		referencecatalogadapter.NewAssets(cdnDomain),
		referencecatalogadapter.NewMemberSummaries(cdnDomain),
		spiceDB,
	)
}

type referenceNoopFileDeleter struct{}

func (referenceNoopFileDeleter) DeleteFileByID(context.Context, string) error { return nil }
func (referenceNoopFileDeleter) ReconcilePublishedEntityAssets(context.Context, managev1.TranscodeEntityType, string) error {
	return nil
}
