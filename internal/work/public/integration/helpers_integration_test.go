//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

var (
	publicIntegrationStack   *testutil.BackendIntegrationStack
	publicIntegrationSpiceDB *auth.SpiceDBClient
	publicIntegrationOnce    sync.Once
	publicIntegrationErr     error
	publicIntegrationMu      sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if publicIntegrationStack != nil {
		if err := publicIntegrationStack.Close(); err != nil && code == 0 {
			fmt.Fprintf(os.Stderr, "close Work public integration stack: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func sharedPublicIntegrationStack() (*testutil.BackendIntegrationStack, error) {
	publicIntegrationOnce.Do(func() {
		publicIntegrationStack, publicIntegrationErr = testutil.StartBackendIntegrationStack(context.Background())
		if publicIntegrationErr == nil {
			publicIntegrationSpiceDB = publicIntegrationStack.SpiceDBClient
		}
	})
	return publicIntegrationStack, publicIntegrationErr
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	publicIntegrationMu.Lock()
	t.Cleanup(publicIntegrationMu.Unlock)
	stack, err := sharedPublicIntegrationStack()
	require.NoError(t, err)
	require.NoError(t, testutil.ResetBackendIntegrationState(t.Context(), stack))
	t.Cleanup(func() { require.NoError(t, testutil.ResetBackendIntegrationState(context.Background(), stack)) })
	return stack.Postgres.DB
}

func seedPublicAdminMemberIdentityLink(t *testing.T, db *gorm.DB, identityID, nickname string) string {
	t.Helper()
	memberID := uuid.NewString()
	var primaryEmail string
	require.NoError(t, db.Raw("SELECT traits->>'email' FROM kratos.identities WHERE id = ?::uuid", identityID).Scan(&primaryEmail).Error)
	require.NotEmpty(t, primaryEmail)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING", identityID).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true,
		PrimaryEmail: &primaryEmail, AvailableEmails: pq.StringArray{primaryEmail},
		SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = publicIntegrationSpiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
	return memberID
}

func publicLegalAdminCtx(memberID, identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		MemberID: auth.MemberID(memberID), IdentityID: auth.IdentityID(identityID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func newPublicWorkRuntimeForTest(db *gorm.DB, cdnDomain string) *workadapter.Runtime {
	planner := og.NewPlanner(db, cdnDomain, sitesettingsadapter.NewRenderConfig(), workadapter.NewProjection())
	return workadapter.NewRuntime(
		db,
		cdnDomain,
		og.NewRefresher(planner, og.NewResolver(workadapter.NewRequests())),
	)
}

func newPublicWorkManageService(t *testing.T, db *gorm.DB, adminID string) *workdomain.WorkService {
	t.Helper()
	return workdomain.NewWorkService(
		db,
		newPublicWorkRuntimeForTest(db, "https://cdn.example.com"),
		publicIntegrationSpiceDB,
		&publicReferenceIdentityManager{identity: &auth.Identity{
			ID: adminID, State: auth.KratosStateActive,
			Traits: map[string]interface{}{"name": "Public Work Admin", "preferred_locale": "en"},
		}},
		publicReferenceAsyncPublisher{},
		workdomain.WithWorkContentBlockStore(newPublicWorkContentBlockStore(t)),
		workdomain.WithWorkContentBlockMediaHydrator(extractedWorkPublicMediaHydrator{}),
		workdomain.WithWorkMemberSummaryLoader(workadapter.NewMemberSummaries(db, "https://cdn.example.com")),
	)
}

type publicReferenceIdentityManager struct{ identity *auth.Identity }

func (m *publicReferenceIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.identity, nil
}
func (m *publicReferenceIdentityManager) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return m.identity, nil
}
func (m *publicReferenceIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return []*auth.Identity{m.identity}, 1, nil
}
func (m *publicReferenceIdentityManager) UpdateIdentityTraits(_ context.Context, _ string, traits map[string]interface{}) error {
	m.identity.Traits = traits
	return nil
}
func (*publicReferenceIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (*publicReferenceIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, map[string]interface{}) error {
	return nil
}
func (*publicReferenceIdentityManager) SetIdentityState(context.Context, string, string) error {
	return nil
}
func (*publicReferenceIdentityManager) DeleteIdentitySessions(context.Context, string) error {
	return nil
}
func (*publicReferenceIdentityManager) DeleteIdentity(context.Context, string) error { return nil }
func (m *publicReferenceIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if m.identity == nil {
		return "", nil
	}
	return m.identity.CurrentEmail(), nil
}

type publicReferenceAsyncPublisher struct{}

func (publicReferenceAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (publicReferenceAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}
func seedCanonicalPublicFileFixture(t *testing.T, db *gorm.DB, fileName, mimeType, assetKind string) (string, string) {
	t.Helper()
	fileID := uuid.NewString()
	extension := model.GetExtensionFromMime(mimeType)
	fileName = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: extension, SHA256: make([]byte, 32), CreatedAt: now,
	}).Error)
	return fileID, seedReadyPublicAssetForFileFixture(t, db, fileID, assetKind)
}

func seedReadyPublicAssetForFileFixture(t *testing.T, db *gorm.DB, fileID, kind string) string {
	t.Helper()
	var file model.File
	require.NoError(t, db.Where("id = ?", fileID).Take(&file).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, file.Extension)
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := file.FileSize
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: kind, ObjectKey: objectKey,
		Extension: file.Extension, MimeType: file.MimeType, FileSize: &fileSize,
		SHA256: append([]byte(nil), file.SHA256...), Disposition: "inline",
		Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return assetID
}

func readyPublicAssetURLForFileFixture(t *testing.T, db *gorm.DB, cdnDomain, fileID string) string {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ? AND status = ?", fileID, model.PublicAssetStatusReady).Take(&asset).Error)
	assetPath, err := mediaauth.AssetPath(asset.ID, asset.Kind, asset.Extension)
	require.NoError(t, err)
	return strings.TrimRight(cdnDomain, "/") + "/" + strings.TrimLeft(assetPath, "/")
}

func seedPublicClientLogoFileFixture(t *testing.T, db *gorm.DB, key, mimeType string) string {
	t.Helper()
	fileID, _ := seedCanonicalPublicFileFixture(t, db, key, mimeType, "logo")
	return fileID
}

func seedPublicWorkShareLink(t *testing.T, db *gorm.DB, workID string) string {
	t.Helper()
	token := uuid.NewString()
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{
		Token: token, EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK.String(),
		EntityID: workID, CreatedAt: time.Now().UTC(), ExpiresAt: &expiresAt,
	}).Error)
	return token
}

func stringPtr(value string) *string { return &value }
