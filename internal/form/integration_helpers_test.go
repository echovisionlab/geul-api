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
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
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

// formIntegrationAudit records personal data access through security and
// the Form domain audit through audit.
type formIntegrationAudit struct {
	security securityaccess.Appender
	audit    domainaudit.Appender
}

func (a formIntegrationAudit) AppendSecurityAccess(ctx context.Context, record sharedtelemetry.SecurityAccessRecord) error {
	return a.security.AppendSecurityAccess(ctx, record)
}

func (a formIntegrationAudit) AppendDomainAuditInTransaction(ctx context.Context, tx *gorm.DB, record sharedtelemetry.AuditRecord) error {
	return a.audit.AppendDomainAuditInTransaction(ctx, tx, record)
}

// newFormUseCasesForIntegration composes the Form use cases the server
// composes. A nil security or audit appender is the durable telemetry
// writer; a nil publisher schedules signals in the mutation transaction.
func newFormUseCasesForIntegration(
	db *gorm.DB,
	security securityaccess.Appender,
	audit domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	publisher contentsignal.Publisher,
) *formdomain.Service {
	if publisher == nil {
		publisher = &mq.Publisher{}
	}
	if security == nil {
		security = apitelemetry.NewDurableWriter(db)
	}
	if audit == nil {
		audit = apitelemetry.NewDurableWriter(db)
	}
	cdnDomain := "https://cdn.example.test"
	contentBlocks, err := contentblock.NewGeneratedStore(formIntegrationFileReuseAuthorizer{})
	if err != nil {
		panic(err)
	}
	service, err := formdomain.NewService(formdomain.Deps{
		DB: db, SpiceDB: spiceDB, Password: integrationFormAuditHasher(), ContentBlocks: contentBlocks,
		Assets: formadapter.NewAssets(cdnDomain), OG: formogadapter.NewOG(cdnDomain, newFormIntegrationOGRefresher(db, cdnDomain)),
		Routes: formadapter.NewRoutes(), Collaboration: formadapter.NewCollaboration(spiceDB),
		Members: memberadapter.PreferredLocales{}, ShareLinks: sharelinkadapter.FormLinks{},
		Signals: publisher, Audit: formIntegrationAudit{security: security, audit: audit},
	})
	if err != nil {
		panic(err)
	}
	return service
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
	_ string,
	security securityaccess.Appender,
	audit domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *formdomain.ConnectHandler {
	t.Helper()
	return formdomain.NewConnectHandler(newFormUseCasesForIntegration(db, security, audit, spiceDB, nil))
}

func newFormServiceForIntegration(db *gorm.DB, _ string, spiceDB *auth.SpiceDBClient) *formdomain.ConnectHandler {
	return formdomain.NewConnectHandler(newFormUseCasesForIntegration(db, nil, nil, spiceDB, nil))
}

func newAuditedInternalFormServiceForIntegration(db *gorm.DB, publisher contentsignal.Publisher, audit domainaudit.Appender, spiceDB *auth.SpiceDBClient) *formdomain.InternalConnectHandler {
	return formdomain.NewInternalConnectHandler(newFormUseCasesForIntegration(db, nil, audit, spiceDB, publisher))
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

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient { return testutil.IntegrationSpiceDB(t) }

// formRequestAuditContext makes ctx a request of its principal, which every
// audited Form mutation attributes its record to.
func formRequestAuditContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	principal := auth.GetUser(ctx)
	require.NotNil(t, principal)
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: principal.IdentityID.String(), MemberID: principal.MemberID.String(), SessionID: principal.SessionID.String(),
	})
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(ctx, request)
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
