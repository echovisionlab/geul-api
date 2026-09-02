//go:build integration

package maptheme

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start map theme integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close map theme integration suite: %v\n", err)
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
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func integrationTestUUID() string { return uuid.NewString() }

func integrationMemberID(identityID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-integration-member:"+identityID)).String()
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
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func postIntegrationAdminCtx(identityID string, _ *string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(integrationMemberID(identityID)),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})
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
			`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`,
			memberID, identityID,
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
			ID: memberID, AccountIdentityID: &identityID, Nickname: name,
			Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
			SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
		}).Error
	}))
	return memberID
}

type failingDomainAuditAppender struct{}

func (failingDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return fmt.Errorf("audit unavailable")
}

func adminCtx() context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: "018f0d9c-1f54-7d51-8abc-a1b2c3d4e5f7",
		MemberID:   "018f0d9c-1f54-7d51-8abc-a1b2c3d4e5f6",
		SessionID:  auth.SessionID(uuid.NewString()), Authenticated: true,
	})
}

func mapThemeAdminUser(t *testing.T) *testutil.OryUser {
	t.Helper()
	return testutil.PrepareOryIntegrationTest(t).CreateUser(t, policyv1.Role.Admin().ID())
}

func mapThemeAdminContext(t *testing.T) context.Context {
	return auth.WithUser(context.Background(), mapThemeAdminUser(t).AuthUserInfo())
}

func mapThemeServiceForTest(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *MapThemeService {
	t.Helper()
	return NewMapThemeService(db, spiceDB)
}

func auditedMapThemeServiceForTest(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	writer domainaudit.Appender,
) *MapThemeService {
	t.Helper()
	return NewAuditedMapThemeService(db, writer, spiceDB)
}

func durableAudienceSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.PrepareOryIntegrationTest(t).SpiceDBClient
}

func withAuditedRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	resolved := *user
	resolved.Onboarded = true
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.99")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), &resolved)
}

func requireResourcePermission(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	resourceID string,
	subject auth.AccountIdentitySubject,
	expected bool,
) {
	t.Helper()
	can, err := policyv1.MapTheme.Manage(resourceID)
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, expected, allowed)
}
