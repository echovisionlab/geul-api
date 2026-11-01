//go:build integration

package legal_test

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediareuse"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

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

// newLegalLocalKratosIntegrationDB is for tests that write legal blocks. The
// collaboration contributor fence locks member JOIN kratos.identities FOR
// SHARE; through postgres_fdw that becomes a REPEATABLE READ lock on every
// active identity in the shared Kratos, so identity updates by concurrent
// tests fail it with serialization errors.
func newLegalLocalKratosIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupOryStackWithLocalKratos(t).DB
}

type legalTestRenderConfig struct{}

func (legalTestRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

// legalIntegration is the production composition of the Terms and Privacy
// use cases over one database: the PostgreSQL Store with its real OG,
// notice, Share Link, Member and contributor adapters.
type legalIntegration struct {
	policies  *legal.Service
	internal  *legal.InternalService
	documents *legal.AIDocumentService
}

func newLegalIntegration(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, audit domainaudit.Appender) legalIntegration {
	t.Helper()
	const cdnDomain = "https://cdn.example.test"
	contents, err := contentblock.NewGeneratedStore(filemediareuse.New(spiceDB))
	require.NoError(t, err)
	units, err := legalpostgres.NewUnits(db, legalpostgres.Ports{
		ContentBlocks: contents,
		Routes:        legaladapter.NewOGRuntime(cdnDomain, ogpostgres.NewPlanner(cdnDomain, legalTestRenderConfig{}, legaladapter.NewProjection())),
		Notices:       legaladapter.NewNoticeRuntime(),
		PreviewLinks:  sharelinkadapter.LegalPreviewLinks{},
		Members:       memberadapter.PreferredLocales{},
		Checkpoints:   testcollaboration.NewCheckpoints(db, spiceDB),
	})
	require.NoError(t, err)
	var integration legalIntegration
	integration.policies, err = legal.NewService(legal.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
		Dispatch: legaladapter.NewNoticeDispatch(db, nil), OGAssets: legaladapter.NewOGAssets(db, cdnDomain),
		BaseURL: "https://example.test",
	})
	require.NoError(t, err)
	integration.internal, err = legal.NewInternalService(legal.InternalDeps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
	})
	require.NoError(t, err)
	integration.documents, err = legal.NewAIDocumentService(legal.AIDocumentDeps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
	})
	require.NoError(t, err)
	return integration
}

func (l legalIntegration) terms() *legal.TermsConnectHandler {
	return legal.NewTermsConnectHandler(l.policies)
}

func (l legalIntegration) privacy() *legal.PrivacyConnectHandler {
	return legal.NewPrivacyConnectHandler(l.policies)
}

func (l legalIntegration) internalTerms() *legal.InternalTermsConnectHandler {
	return legal.NewInternalTermsConnectHandler(l.internal)
}

func (l legalIntegration) internalPrivacy() *legal.InternalPrivacyConnectHandler {
	return legal.NewInternalPrivacyConnectHandler(l.internal)
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

// seedBulkEmailAudienceIdentity stores a newsletter subscriber a legal
// notice reaches.
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
	memberID := testutil.PostIntegrationMemberID(identityID)
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
