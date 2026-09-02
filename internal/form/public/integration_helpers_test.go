//go:build integration

package public

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedFormPublicMemberIdentityLink(t *testing.T, db *gorm.DB, identityID, nickname string) string {
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
	testutil.GrantIntegrationGlobalRole(t, testutil.IntegrationSpiceDB(t), identityID, policyv1.Role.User())
	return memberID
}

func seedFormPublicAdminMemberIdentityLink(t *testing.T, db *gorm.DB, identityID, nickname string) string {
	t.Helper()
	memberID := seedFormPublicMemberIdentityLink(t, db, identityID, nickname)
	testutil.GrantIntegrationGlobalRole(t, testutil.IntegrationSpiceDB(t), identityID, policyv1.Role.Admin())
	return memberID
}

func formPublicPrincipalContext(memberID, identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		MemberID: auth.MemberID(memberID), IdentityID: auth.IdentityID(identityID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func stringPtr(value string) *string    { return &value }
func boolPtr(value bool) *bool          { return &value }
func publicInt32Ptr(value int32) *int32 { return &value }

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
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: assetKind, ObjectKey: objectKey,
		Extension: extension, MimeType: mimeType, FileSize: &fileSize, SHA256: make([]byte, 32),
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID, assetID
}

func readyPublicAssetURLForFileFixture(t *testing.T, db *gorm.DB, cdnDomain, fileID string) string {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ? AND status = ?", fileID, model.PublicAssetStatusReady).Take(&asset).Error)
	ref, err := mediaasset.NewLifecycle(db, cdnDomain).AssetRef(asset)
	require.NoError(t, err)
	return ref.GetUrl()
}

func seedBoundOgAssetFixture(t *testing.T, db *gorm.DB, ownerType, ownerID, bindingKey string) (string, string) {
	t.Helper()
	fileID, assetID := seedCanonicalPublicFileFixture(t, db, "og.webp", "image/webp", "og")
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: assetID, OwnerType: ownerType, OwnerID: ownerID, BindingKey: bindingKey,
		SourceFileID: &fileID, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return assetID, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", fileID)
}

type formPublicIdentityManager struct{ identity *auth.Identity }

func (m formPublicIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.identity, nil
}
func (m formPublicIdentityManager) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return m.identity, nil
}
func (formPublicIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (formPublicIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}
func (formPublicIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (formPublicIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (formPublicIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }
func (formPublicIdentityManager) DeleteIdentitySessions(context.Context, string) error   { return nil }
func (formPublicIdentityManager) DeleteIdentity(context.Context, string) error           { return nil }
func (m formPublicIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if m.identity == nil {
		return "", nil
	}
	return m.identity.CurrentEmail(), nil
}

type formPublicRenderConfig struct{}

type formPublicFileReuseAuthorizer struct{}

func (formPublicFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func (formPublicRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func newFormPublicOGRefresher(db *gorm.DB, cdnDomain string) *og.Refresher {
	planner := og.NewPlanner(db, cdnDomain, formPublicRenderConfig{}, formogadapter.NewProjection())
	return og.NewRefresher(planner, og.NewResolver(formogadapter.NewRequests()))
}

func newPublicManageFormService(t *testing.T, db *gorm.DB, adminIdentityID string) *formdomain.FormService {
	t.Helper()
	assets := formadapter.NewAssets("https://cdn.example.com")
	contentBlocks, err := contentblock.NewGeneratedStore(formPublicFileReuseAuthorizer{})
	require.NoError(t, err)
	return formdomain.NewFormService(
		db,
		crypto.NewPasswordHasher(&crypto.Argon2idParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}),
		formPublicIdentityManager{identity: &auth.Identity{ID: adminIdentityID, State: auth.KratosStateActive, Traits: structured.Fields{"preferred_locale": "en"}}},
		testutil.IntegrationSpiceDB(t),
		formdomain.Dependencies{
			ContentBlocks: contentBlocks,
			Assets:        assets, PublicAssets: assets, Routes: formadapter.NewRoutes(), Translation: formadapter.NewTranslation(),
			OG: formogadapter.NewOG("https://cdn.example.com", newFormPublicOGRefresher(db, "https://cdn.example.com")),
		},
	)
}
