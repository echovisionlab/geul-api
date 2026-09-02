//go:build integration

package form_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type formIntegrationIdentityManager struct{ identity *auth.Identity }

func (m formIntegrationIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.identity, nil
}
func (m formIntegrationIdentityManager) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return m.identity, nil
}
func (formIntegrationIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (formIntegrationIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}
func (formIntegrationIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (formIntegrationIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (formIntegrationIdentityManager) SetIdentityState(context.Context, string, string) error {
	return nil
}
func (formIntegrationIdentityManager) DeleteIdentitySessions(context.Context, string) error {
	return nil
}
func (formIntegrationIdentityManager) DeleteIdentity(context.Context, string) error { return nil }
func (m formIntegrationIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if m.identity == nil {
		return "", nil
	}
	return m.identity.CurrentEmail(), nil
}

type formIntegrationRenderConfig struct{}

func (formIntegrationRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func newFormIntegrationOGRefresher(db *gorm.DB, cdnDomain string) *og.Refresher {
	planner := og.NewPlanner(db, cdnDomain, formIntegrationRenderConfig{}, formogadapter.NewProjection())
	return og.NewRefresher(planner, og.NewResolver(formogadapter.NewRequests()))
}

func formIntegrationDependencies(db *gorm.DB, cdnDomain string, security securityaccess.Appender) formdomain.Dependencies {
	if security == nil {
		security = apitelemetry.NewDurableWriter(db)
	}
	assets := formadapter.NewAssets(cdnDomain)
	contentBlocks, err := contentblock.NewGeneratedStore(formIntegrationFileReuseAuthorizer{})
	if err != nil {
		panic(err)
	}
	return formdomain.Dependencies{
		ContentBlocks: contentBlocks,
		Assets:        assets, PublicAssets: assets,
		OG:     formogadapter.NewOG(cdnDomain, newFormIntegrationOGRefresher(db, cdnDomain)),
		Routes: formadapter.NewRoutes(), Translation: formadapter.NewTranslation(),
		SecurityAccess: formadapter.NewSecurityAccess(security),
	}
}

type formIntegrationFileReuseAuthorizer struct{}

func (formIntegrationFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func newAuditedFormServiceForIntegration(
	t *testing.T,
	db *gorm.DB,
	identityID string,
	security securityaccess.Appender,
	audit domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *formdomain.FormService {
	t.Helper()
	return formdomain.NewAuditedFormService(
		db, integrationFormAuditHasher(),
		formIntegrationIdentityManager{identity: &auth.Identity{ID: identityID, State: auth.KratosStateActive, Traits: structured.Fields{"preferred_locale": "en"}}},
		audit, spiceDB, formIntegrationDependencies(db, "https://cdn.example.test", security),
	)
}

func newFormServiceForIntegration(
	db *gorm.DB,
	identityID string,
	spiceDB *auth.SpiceDBClient,
) *formdomain.FormService {
	return formdomain.NewFormService(
		db, integrationFormAuditHasher(),
		formIntegrationIdentityManager{identity: &auth.Identity{ID: identityID, State: auth.KratosStateActive, Traits: structured.Fields{"preferred_locale": "en"}}},
		spiceDB, formIntegrationDependencies(db, "https://cdn.example.test", nil),
	)
}

func newAuditedInternalFormServiceForIntegration(db *gorm.DB, publisher formdomain.AsyncPublisher, audit domainaudit.Appender, spiceDB *auth.SpiceDBClient) *formdomain.InternalFormService {
	return formdomain.NewAuditedInternalFormService(db, publisher, audit, spiceDB, formIntegrationDependencies(db, "", nil))
}

type failingFormAuditAppender struct{}

func (failingFormAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("form audit unavailable")
}

type formAuditRow struct {
	Action        string `gorm:"column:action"`
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func integrationFormSchema() []byte {
	return []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email address","type":"email"}]}]}`)
}

func seedFormImageBindingFile(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(key))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, fileID, "form-"+fileID, digest[:]).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "image", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize, SHA256: digest[:],
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID
}

func newServiceIntegrationDB(t *testing.T) *gorm.DB       { return testutil.NewIntegrationDB(t) }
func integrationTestUUID() string                         { return testutil.IntegrationUUID() }
func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient { return testutil.IntegrationSpiceDB(t) }
func grantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, role)
}

func seedExternalKratosIdentityWithTraits(t *testing.T, db *gorm.DB, identityID, nickname string) string {
	t.Helper()
	email := identityID + "@form.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Name: nickname, Email: email})
	memberID := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING", identityID).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true, PrimaryEmail: &email, AvailableEmails: pq.StringArray{email}, SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	return memberID
}

func ptrString(value string) *string { return &value }

func seedFormSourceLocaleBaseRowAt(t *testing.T, db *gorm.DB, now time.Time) string {
	t.Helper()
	formID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?, 'compact')", documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO form (id, status, is_public, source_locale, content_document_id, created_at, updated_at) VALUES (?, 'FORM_STATUS_DRAFT', FALSE, 'en', ?, ?, ?)`, formID, documentID, now, now).Error)
	spiceDB := integrationSpiceDB(t)
	policy, err := policyv1.Form.TouchPolicy(formID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() {
		deletePolicy, deleteErr := policyv1.Form.DeletePolicy(formID)
		require.NoError(t, deleteErr)
		_, cleanupErr := spiceDB.ApplyRelationships(context.Background(), deletePolicy)
		require.NoError(t, cleanupErr)
	})
	return formID
}

func seedGuardedFormLocales(t *testing.T, db *gorm.DB, now time.Time) string {
	t.Helper()
	formID := seedFormSourceLocaleBaseRowAt(t, db, now)
	require.NoError(t, db.Table("form").Where("id = ?", formID).Update("slug", "original-slug").Error)
	sourceSchema := []byte(`{"id":"source-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email","required":true}]}]}`)
	targetSchema := []byte(`{"id":"source-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email","required":true}]}]}`)
	seedFormSourceLocaleTranslationRow(t, db, formID, "en", "Contact", sourceSchema, ptrString("Contact\nEmail"), now)
	seedFormSourceLocaleTranslationRow(t, db, formID, "ko", "문의 양식", targetSchema, ptrString("문의\n이메일"), now)
	return formID
}

func seedFormSourceLocaleTranslationRow(t *testing.T, db *gorm.DB, formID, locale, title string, contentJSON []byte, contentText *string, now time.Time) {
	t.Helper()
	var titleValue, contentJSONValue structured.Value
	if title != "" {
		titleValue = title
	}
	if len(contentJSON) > 0 {
		contentJSONValue = string(contentJSON)
	}
	require.NoError(t, db.Exec(`INSERT INTO form_translation (
		entity_id, locale, title, content_json, content_text,
		created_at, updated_at
	) VALUES (?, ?, ?, CAST(? AS jsonb), ?, ?, ?)`,
		formID, locale, titleValue, contentJSONValue, contentText, now, now).Error)
}
