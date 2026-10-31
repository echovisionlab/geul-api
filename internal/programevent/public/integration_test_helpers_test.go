//go:build integration

package public

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"
	eventdomain "github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// publicIntegrationSpiceDB returns the SpiceDB client of the test's own Ory stack.
func publicIntegrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func newPublicProgramEventAssets(db *gorm.DB, cdnDomain string) Assets {
	return programeventadapter.NewPublicAssets(db, cdnDomain)
}

func newManageProgramEventRuntime(cdnDomain string) *programeventadapter.Runtime {
	return programeventadapter.NewRuntime(cdnDomain)
}

// manageProgramEvents builds the administrator Program Event use cases that
// seed public reads. Collaborators left unset in deps get stand-ins: a
// recording audit appender, the File gateway, accepted signals and
// collaboration checkpoints.
func manageProgramEvents(t *testing.T, db *gorm.DB, deps eventdomain.Deps) *eventdomain.Service {
	t.Helper()
	spiceDB := publicIntegrationSpiceDB(t)
	deps.DB, deps.SpiceDB = db, spiceDB
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.ContentBlocks == nil {
		store, err := contentblock.NewGeneratedStore(publicProgramEventFileReuseAuthorizer{})
		require.NoError(t, err)
		deps.ContentBlocks = store
	}
	if deps.Signals == nil {
		deps.Signals = publicProgramEventSignals{}
	}
	if deps.Media == nil {
		deps.Media = newManageProgramEventRuntime("https://cdn.example.com")
	}
	if deps.MediaHydrator == nil {
		deps.MediaHydrator = publicProgramEventFileGateway{db: db}
	}
	if deps.CreditMembers == nil {
		deps.CreditMembers = programeventadapter.NewCreditMemberSummaries(db, "https://cdn.example.com")
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = testcollaboration.NewCheckpoints(db, spiceDB)
	}
	service, err := eventdomain.NewService(deps)
	require.NoError(t, err)
	return service
}

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start public Program Event integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close public Program Event integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func seedPublicAdminMemberIdentityLink(t *testing.T, db *gorm.DB, identityID, nickname string) string {
	t.Helper()
	memberID := uuid.NewString()
	var primaryEmail string
	require.NoError(t, db.Raw(
		"SELECT traits->>'email' FROM kratos.identities WHERE id = ?::uuid", identityID,
	).Scan(&primaryEmail).Error)
	require.NotEmpty(t, primaryEmail)
	require.NoError(t, db.Exec(
		"INSERT INTO account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING", identityID,
	).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true,
		PrimaryEmail: &primaryEmail, AvailableEmails: pq.StringArray{primaryEmail},
		SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID,
	).Error)
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = publicIntegrationSpiceDB(t).SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
	return memberID
}

func publicPrincipalContext(memberID, identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		MemberID: auth.MemberID(memberID), IdentityID: auth.IdentityID(identityID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func stringPtr(value string) *string { return &value }
func boolPtr(value bool) *bool       { return &value }
