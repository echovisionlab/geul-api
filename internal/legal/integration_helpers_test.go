//go:build integration

package legal_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type failingDomainAuditAppender struct{}

var _ domainaudit.Appender = failingDomainAuditAppender{}

func (failingDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

type noopDomainAuditAppender struct{}

var _ domainaudit.Appender = noopDomainAuditAppender{}

func (noopDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return nil
}

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Legal integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Legal integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newLegalIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.NewIntegrationDB(t)
}

type legalTestRenderConfig struct{}

func (legalTestRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func newOGPlannerForTest(db *gorm.DB, cdnDomain string) *og.Planner {
	return og.NewPlanner(db, cdnDomain, legalTestRenderConfig{}, legaladapter.NewProjection())
}

func legalIntegrationAdminCtxWithIdentityAndSpiceDB(
	t *testing.T,
	db *gorm.DB,
) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.99")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), user), spiceDB
}

func ptrString(value string) *string { return &value }

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func publishEmailTemplateSourceBlocksForIntegration(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	templateID string,
	text string,
) {
	t.Helper()
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	var template struct {
		ContentDocumentID string `gorm:"column:content_document_id"`
		SourceLocale      string `gorm:"column:source_locale"`
	}
	require.NoError(t, db.Table("email_template").Select("content_document_id", "source_locale").Where(
		"id = ?", templateID,
	).Take(&template).Error)
	documentID, err := uuid.Parse(template.ContentDocumentID)
	require.NoError(t, err)
	snapshot, err := store.LoadSnapshot(
		t.Context(), db, documentID, template.SourceLocale,
	)
	require.NoError(t, err)
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	require.NoError(t, err)
	contributorID := testutil.IntegrationUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, spiceDB, contributorID)
	_, err = emailauthoring.NewAuditedInternalEmailTemplateService(
		db,
		apitelemetry.NewDurableWriter(db),
		spiceDB,
		emailauthoring.WithInternalEmailTemplateContentBlockStore(store),
		emailauthoring.WithInternalEmailTemplateCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
		emailauthoring.WithInternalEmailTemplateCampaignDeliveryReferences(
			emailauthoringadapter.NewCampaignDeliveryReferences(),
		),
	).ApplyBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
		EmailTemplateId: templateID,
		Locale:          template.SourceLocale,
		Batch: testutil.NewParagraphBatch(
			document,
			snapshot.Document.Revision.String(),
			template.SourceLocale,
			text,
			[]string{contributorID},
		),
	}))
	require.NoError(t, err)
}

func requireRelationWhereCount(
	t *testing.T,
	db *gorm.DB,
	tableName string,
	where string,
	want int64,
	args ...any,
) {
	t.Helper()
	var count int64
	result := db.Raw(`SELECT COUNT(*) FROM `+tableName+` WHERE `+where, args...).Scan(&count)
	require.NoError(t, result.Error)
	require.Equal(t, want, count)
}

func integrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-legal-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func seedExternalKratosIdentityWithTraits(
	t *testing.T,
	db *gorm.DB,
	identityID string,
	name string,
) string {
	t.Helper()
	email := identityID + "@legal-integration.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: name, CreatedAt: time.Now().UTC(),
	})
	memberID := integrationMemberID(identityID)
	require.NoError(t, db.Exec(`
		INSERT INTO account_identity (id, created_at)
		SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
		ON CONFLICT (id) DO NOTHING
	`, identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO public.member (
			id, account_identity_id, nickname, onboarded,
			primary_email, available_emails, created_at, updated_at
		) VALUES (?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?]::text[], NOW(), NOW())
	`, memberID, identityID, name, email, email).Error)
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
		memberID, identityID,
	).Error)
	return memberID
}

func ensureBulkEmailAudienceKratosIdentityColumns(t *testing.T, db *gorm.DB) {
	t.Helper()
	testutil.EnsureKratosIdentityFixtureColumns(t, db)
}

func seedBulkEmailAudienceIdentity(
	t *testing.T,
	db *gorm.DB,
	email string,
	createdAt time.Time,
) string {
	t.Helper()
	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: "Legal notice recipient", CreatedAt: createdAt,
	})
	memberID := integrationMemberID(identityID)
	require.NoError(t, db.Exec(`
		INSERT INTO account_identity (id, created_at)
		SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
		ON CONFLICT (id) DO NOTHING
	`, identityID).Error)
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: "Legal notice recipient " + memberID,
		Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
		SocialLinks: map[string]string{}, CreatedAt: createdAt, UpdatedAt: createdAt,
	}).Error)
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
		memberID, identityID,
	).Error)
	require.NoError(t, db.Create(&model.NewsletterSubscription{
		IdentityID: identityID, SubscribedAt: createdAt,
	}).Error)
	return identityID
}

type recordingLegalEmailPublisher struct {
	sendBulkJobs []*managev1.SendBulkEmailBatchEvent
}

func (p *recordingLegalEmailPublisher) EnqueueProtobuf(
	_ context.Context,
	_ string,
	_ string,
	message proto.Message,
) error {
	if bulk, ok := message.(*managev1.SendBulkEmailBatchEvent); ok {
		p.sendBulkJobs = append(p.sendBulkJobs, bulk)
	}
	return nil
}

func (*recordingLegalEmailPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (p *recordingLegalEmailPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if executor == nil {
		return fmt.Errorf("transactional executor is required")
	}
	return p.EnqueueProtobuf(ctx, queue, messageID, message)
}

func (p *recordingLegalEmailPublisher) PublishSendBulkEmail(
	_ context.Context,
	job *managev1.SendBulkEmailBatchEvent,
) error {
	p.sendBulkJobs = append(p.sendBulkJobs, job)
	return nil
}

func grantIntegrationGlobalRole(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	identityID string,
	role policyv1.RoleID,
) {
	t.Helper()
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, role)
}

func integrationTestUUID() string { return testutil.IntegrationUUID() }
