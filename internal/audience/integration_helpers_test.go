//go:build integration

package audience_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
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

func newAudienceServiceForTest(db *gorm.DB, spiceDB *auth.SpiceDBClient) *audiencedomain.Service {
	return newAuditedAudienceServiceForTest(db, &audittest.Recorder{}, spiceDB)
}

func newAuditedAudienceServiceForTest(
	db *gorm.DB,
	writer domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *audiencedomain.Service {
	service, err := audiencedomain.NewService(audiencedomain.Deps{
		DB:               db,
		SpiceDB:          spiceDB,
		RecipientCounter: audienceRecipientCounter{},
		MemberReferences: audienceMemberReferences{},
		Audit:            writer,
	})
	if err != nil {
		panic(err)
	}
	return service
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

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
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

type audienceAvatarFiles struct{}

func (audienceAvatarFiles) PromoteUserAvatarAsset(context.Context, string) (*commonv1.AssetRef, error) {
	return nil, errors.New("audience tests do not promote avatars")
}

type noopMemberEmailPublisher struct{}

func (noopMemberEmailPublisher) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return nil
}

func (noopMemberEmailPublisher) PublishSendEmailWithExecutor(context.Context, eventpkg.DBTX, *managev1.SendEmailEvent) error {
	return nil
}
