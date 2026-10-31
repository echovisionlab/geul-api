//go:build integration

package maptheme

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
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

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func postIntegrationAdminCtx(identityID string, _ *string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(testutil.PostIntegrationMemberID(identityID)),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})
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
