//go:build integration

package public_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/programevent/public"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"
	eventdomain "github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	programeventpublicpostgres "github.com/echovisionlab/geul-api/internal/programevent/public/postgres"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
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

func newPublicProgramEventAssets(db *gorm.DB, cdnDomain string) public.Assets {
	return programeventadapter.NewPublicAssets(db, cdnDomain)
}

func newManageProgramEventRuntime(cdnDomain string) *programeventadapter.Runtime {
	return programeventadapter.NewRuntime(cdnDomain)
}

// manageDeps are the administrator collaborators a test overrides.
type manageDeps struct {
	Audit         domainaudit.Appender
	ContentBlocks *contentblock.Store
	Media         programeventpostgres.Media
	MediaHydrator programeventpostgres.Files
}

// manageProgramEvents builds the administrator Program Event use cases that
// seed public reads. Collaborators left unset in deps get stand-ins: a
// recording audit appender, the File gateway, accepted signals and
// collaboration checkpoints.
func manageProgramEvents(t *testing.T, db *gorm.DB, deps manageDeps) *eventdomain.Service {
	t.Helper()
	spiceDB := publicIntegrationSpiceDB(t)
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.ContentBlocks == nil {
		store, err := contentblock.NewGeneratedStore(publicProgramEventFileReuseAuthorizer{})
		require.NoError(t, err)
		deps.ContentBlocks = store
	}
	if deps.Media == nil {
		deps.Media = newManageProgramEventRuntime("https://cdn.example.com")
	}
	if deps.MediaHydrator == nil {
		deps.MediaHydrator = publicProgramEventFileGateway{db: db}
	}
	units, err := programeventpostgres.NewUnits(db, programeventpostgres.Ports{
		ContentBlocks: deps.ContentBlocks, Signals: publicProgramEventSignals{}, Media: deps.Media,
		Files: deps.MediaHydrator, Members: memberadapter.ContentMembers{}, CreditArtists: artistadapter.CreditArtists{},
		Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB),
	})
	require.NoError(t, err)
	service, err := eventdomain.NewService(eventdomain.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit,
		CreditMembers: programeventadapter.NewCreditMemberSummaries(db, "https://cdn.example.com"),
	})
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

// publicDeps are the public collaborators a test sets; DB and
// ContentBlocks build the Store.
type publicDeps struct {
	DB            *gorm.DB
	ContentBlocks *contentblock.Store
	Assets        public.Assets
	Files         public.MediaHydrator
	CreditMembers public.CreditMemberSummaries
}

// publicServiceForTest builds the public reads through NewService. Fields
// left unset in deps get stand-ins that fail if a test reaches them.
func publicServiceForTest(t *testing.T, deps publicDeps) *public.Service {
	t.Helper()
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	if deps.Assets == nil {
		deps.Assets = unusedAssets{}
	}
	if deps.Files == nil {
		deps.Files = unusedFiles{}
	}
	if deps.CreditMembers == nil {
		deps.CreditMembers = unusedCreditMembers{}
	}
	units, err := programeventpublicpostgres.NewUnits(deps.DB, deps.ContentBlocks)
	require.NoError(t, err)
	service, err := public.NewService(public.Deps{Units: units, Assets: deps.Assets, Files: deps.Files, CreditMembers: deps.CreditMembers})
	require.NoError(t, err)
	return service
}

var errUnusedTestDependency = errors.New("test did not configure this public Program Event dependency")

type unusedAssets struct{}

func (unusedAssets) ResolveReadyAssetForSourceFile(context.Context, string, ...string) *commonv1.AssetRef {
	return nil
}

func (unusedAssets) ResolveReadyAssetsForSourceFiles(context.Context, []string, ...string) (map[string]*commonv1.AssetRef, error) {
	return nil, errUnusedTestDependency
}

type unusedFiles struct{}

func (unusedFiles) HydrateAuthorizedContentBlockMedia(context.Context, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return nil, errUnusedTestDependency
}

type unusedCreditMembers struct{}

func (unusedCreditMembers) LoadPublicCreditMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error) {
	return nil, errUnusedTestDependency
}
