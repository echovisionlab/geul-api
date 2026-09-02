//go:build integration

package audience_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type audienceRecipientCounter struct{}

func (audienceRecipientCounter) Count(context.Context, *model.AudienceSegment) (int64, error) {
	return 0, nil
}

type audienceMemberReferences struct{}

func (audienceMemberReferences) EligibleIDs(
	ctx context.Context,
	db *gorm.DB,
	memberIDs []string,
) ([]string, error) {
	return authorizationtarget.EligibleMemberIDs(ctx, db, memberIDs)
}

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Audience integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Audience integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newAudienceServiceForTest(db *gorm.DB, spiceDB *auth.SpiceDBClient) *audiencedomain.AudienceService {
	return audiencedomain.NewAudienceService(
		db,
		spiceDB,
		audienceRecipientCounter{},
		audienceMemberReferences{},
	)
}

func newAuditedAudienceServiceForTest(
	db *gorm.DB,
	writer domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *audiencedomain.AudienceService {
	return audiencedomain.NewAuditedAudienceService(
		db,
		writer,
		spiceDB,
		audienceRecipientCounter{},
		audienceMemberReferences{},
	)
}

func newAudienceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.NewIntegrationDB(t)
}

func newAudienceConcurrentIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	db, err := gorm.Open(gormpostgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func createAudienceCampaignFixture(t *testing.T, db *gorm.DB, campaign *model.Campaign) {
	t.Helper()
	documentID := uuid.NewString()
	campaign.ContentDocumentID = &documentID
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'email')`,
			documentID,
		).Error; err != nil {
			return err
		}
		return tx.Create(campaign).Error
	}))
}

func audienceCampaignUpdatedAtFixture(t *testing.T, db *gorm.DB, campaignID string) time.Time {
	t.Helper()
	var campaign struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("campaign").
		Select("updated_at").
		Where("id = ?", campaignID).
		Take(&campaign).Error)
	return campaign.UpdatedAt
}

func audienceCampaignRenderSnapshotFixture(subject, contentHTML string) model.JSONFields {
	return model.JSONFields{
		"subject":       subject,
		"content_html":  contentHTML,
		"source_locale": "en",
		"translations": []any{model.JSONFields{
			"locale":       "en",
			"subject":      subject,
			"content_html": contentHTML,
		}},
	}
}

func integrationTestUUID() string { return uuid.NewString() }

func integrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-audience-integration-member:"+identityID))
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
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, role)
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
			memberID,
			identityID,
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
			ID: memberID, AccountIdentityID: &identityID, Nickname: name, Onboarded: true,
			PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{},
			CreatedAt: now, UpdatedAt: now,
		}).Error
	}))
	return memberID
}

type audienceIdentityManager struct{}

func (audienceIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return nil, nil
}
func (audienceIdentityManager) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return nil, nil
}
func (audienceIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (audienceIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	return "", nil
}
func (audienceIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}
func (audienceIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (audienceIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (audienceIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }
func (audienceIdentityManager) DeleteIdentitySessions(context.Context, string) error   { return nil }
func (audienceIdentityManager) DeleteIdentity(context.Context, string) error           { return nil }

type audienceNoopFileDeleter struct{}

func (audienceNoopFileDeleter) DeleteFileByID(context.Context, string) error { return nil }

type noopMemberEmailPublisher struct{}

func (noopMemberEmailPublisher) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return nil
}
