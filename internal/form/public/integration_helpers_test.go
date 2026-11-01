//go:build integration

package public

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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

// formPublicPrincipalContext is a request of the Member, which the audited
// management mutations that seed public fixtures attribute their records to.
func formPublicPrincipalContext(memberID, identityID string) context.Context {
	sessionID := uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: identityID, MemberID: memberID, SessionID: sessionID,
	})
	if err != nil {
		panic(err)
	}
	return auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), request), &auth.UserInfo{
		MemberID: auth.MemberID(memberID), IdentityID: auth.IdentityID(identityID),
		SessionID: auth.SessionID(sessionID), Authenticated: true, Onboarded: true,
	})
}

func stringPtr(value string) *string    { return &value }
func boolPtr(value bool) *bool          { return &value }
func publicInt32Ptr(value int32) *int32 { return &value }

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
	fileID, assetID := testutil.SeedCanonicalPublicFileFixture(t, db, "og.webp", "image/webp", "og")
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

func newPublicManageFormService(t *testing.T, db *gorm.DB, _ string) *formdomain.ConnectHandler {
	t.Helper()
	contentBlocks, err := contentblock.NewGeneratedStore(formPublicFileReuseAuthorizer{})
	require.NoError(t, err)
	spiceDB := testutil.IntegrationSpiceDB(t)
	forms, err := formdomain.NewService(formdomain.Deps{
		DB:            db,
		SpiceDB:       spiceDB,
		Password:      crypto.NewPasswordHasher(&crypto.Argon2idParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}),
		ContentBlocks: contentBlocks,
		Assets:        formadapter.NewAssets("https://cdn.example.com"),
		OG:            formogadapter.NewOG("https://cdn.example.com", newFormPublicOGRefresher(db, "https://cdn.example.com")),
		Routes:        formadapter.NewRoutes(),
		Collaboration: formadapter.NewCollaboration(spiceDB),
		Members:       memberadapter.PreferredLocales{},
		ShareLinks:    sharelinkadapter.FormLinks{},
		Signals:       &mq.Publisher{},
		Audit:         apitelemetry.NewDurableWriter(db),
	})
	require.NoError(t, err)
	return formdomain.NewConnectHandler(forms)
}

// newPublicFormHandlerForIntegration composes the public Form boundary the
// server composes; a nil audit appender is the durable telemetry writer.
func newPublicFormHandlerForIntegration(t *testing.T, db *gorm.DB, password *crypto.PasswordHasher, audit domainaudit.Appender) *ConnectHandler {
	t.Helper()
	if audit == nil {
		audit = apitelemetry.NewDurableWriter(db)
	}
	forms, err := NewService(Deps{
		DB: db, SpiceDB: testutil.IntegrationSpiceDB(t), Password: password,
		Assets:  formadapter.NewAssets("https://cdn.example.com"),
		Members: memberadapter.PreferredLocales{}, ShareLinks: sharelinkadapter.FormLinks{}, Audit: audit,
	})
	require.NoError(t, err)
	return NewConnectHandler(forms)
}
