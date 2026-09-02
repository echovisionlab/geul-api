//go:build integration

package testutil

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestOryIntegrationSuiteResetsKratosPostgresAndSpiceDBBetweenTests(t *testing.T) {
	suite := setupOryIntegrationSuiteTest(t)
	ActivateOryIntegrationSuite(suite)
	t.Cleanup(func() { DeactivateOryIntegrationSuite(suite) })

	const fixedMemberID = "00000000-0000-4000-8000-000000000111"
	var identityID string
	t.Run("writes committed state", func(t *testing.T) {
		stack := SetupOryStack(t)
		require.Same(t, suite.Stack(), stack)
		user := stack.CreateUser(t, policyv1.Role.Author().ID())
		identityID = user.IdentityID
		require.NoError(t, stack.DB.Exec(`
			INSERT INTO member (id, nickname, onboarded, primary_email, available_emails)
			VALUES (?::uuid, 'fixed-reset-member', TRUE, 'fixed-reset@example.test', ARRAY['fixed-reset@example.test']::text[])
		`, fixedMemberID).Error)
	})
	t.Run("starts from exact baseline", func(t *testing.T) {
		stack := SetupOryStack(t)
		var memberCount, identityCount int64
		require.NoError(t, stack.DB.Table("member").Where("id = ?::uuid", fixedMemberID).Count(&memberCount).Error)
		require.Zero(t, memberCount)
		require.NoError(t, stack.DB.Table("kratos.identities").Where("id = ?::uuid", identityID).Count(&identityCount).Error)
		require.Zero(t, identityCount)

		raw, err := newIntegrationSpiceDBRawClient(stack.SpiceDBEndpoint, stack.SpiceDBToken)
		require.NoError(t, err)
		defer raw.Close()
		definitions, err := integrationSpiceDBDefinitionNames()
		require.NoError(t, err)
		require.NoError(t, verifyIntegrationSpiceDBBaseline(t.Context(), raw, definitions))
	})
}

func TestIntegrationSuiteFixedIdentifiersCanBeReusedAfterReset(t *testing.T) {
	suite := setupOryIntegrationSuiteTest(t)
	ActivateOryIntegrationSuite(suite)
	t.Cleanup(func() { DeactivateOryIntegrationSuite(suite) })

	fixedID := uuid.NewString()
	t.Run("first", func(t *testing.T) {
		stack := SetupOryStack(t)
		require.NoError(t, stack.DB.Exec(`INSERT INTO member (id, nickname) VALUES (?::uuid, 'repeatable')`, fixedID).Error)
	})
	t.Run("second", func(t *testing.T) {
		stack := SetupOryStack(t)
		require.NoError(t, stack.DB.Exec(`INSERT INTO member (id, nickname) VALUES (?::uuid, 'repeatable')`, fixedID).Error)
	})
}

func TestOryIntegrationSuiteRestoresCommittedPostgresStateAndBootstrapRows(t *testing.T) {
	suite := setupOryIntegrationSuiteTest(t)
	ActivateOryIntegrationSuite(suite)
	t.Cleanup(func() { DeactivateOryIntegrationSuite(suite) })

	baseline := integrationBaselineCounts(t, suite.Stack().DB)
	assertRequiredIntegrationBaseline(t, baseline)
	const fixedMemberID = "00000000-0000-4000-8000-000000000222"
	const fixedAccountIdentityID = "00000000-0000-4000-8000-000000000223"
	var kratosIdentityID string
	t.Run("writes committed state through the base handle and a separate connection", func(t *testing.T) {
		stack := PrepareOryIntegrationTest(t)
		require.Same(t, suite.Stack(), stack)
		kratosIdentityID = stack.CreateUser(t, policyv1.Role.Author().ID()).IdentityID
		committed, err := gorm.Open(postgres.Open(stack.PostgresDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		committedSQL, err := committed.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, committedSQL.Close()) })
		require.NoError(t, committed.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, fixedAccountIdentityID).Error)
		require.NoError(t, committed.Exec(`
			INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails)
			VALUES (?::uuid, ?::uuid, 'committed-reset-member', TRUE, 'committed-reset@example.test', ARRAY['committed-reset@example.test']::text[])
		`, fixedMemberID, fixedAccountIdentityID).Error)
	})
	t.Run("restores schema baseline and removes committed state", func(t *testing.T) {
		stack := SetupOryStack(t)
		var memberCount, accountIdentityCount, kratosIdentityCount int64
		require.NoError(t, stack.DB.Table("member").Where("id = ?::uuid", fixedMemberID).Count(&memberCount).Error)
		require.NoError(t, stack.DB.Table("account_identity").Where("id = ?::uuid", fixedAccountIdentityID).Count(&accountIdentityCount).Error)
		require.NoError(t, stack.DB.Table("kratos.identities").Where("id = ?::uuid", kratosIdentityID).Count(&kratosIdentityCount).Error)
		require.Zero(t, memberCount)
		require.Zero(t, accountIdentityCount)
		require.Zero(t, kratosIdentityCount)
		require.Equal(t, baseline, integrationBaselineCounts(t, suite.Stack().DB))

		raw, err := newIntegrationSpiceDBRawClient(stack.SpiceDBEndpoint, stack.SpiceDBToken)
		require.NoError(t, err)
		defer raw.Close()
		definitions, err := integrationSpiceDBDefinitionNames()
		require.NoError(t, err)
		require.NoError(t, verifyIntegrationSpiceDBBaseline(t.Context(), raw, definitions))
	})
}

func TestOryIntegrationSuiteParentAndChildShareLease(t *testing.T) {
	suite := setupOryIntegrationSuiteTest(t)
	ActivateOryIntegrationSuite(suite)
	t.Cleanup(func() { DeactivateOryIntegrationSuite(suite) })

	const memberID = "00000000-0000-4000-8000-000000000333"
	parent := SetupOryStack(t)
	require.NoError(t, parent.DB.Exec(`INSERT INTO member (id, nickname) VALUES (?::uuid, 'parent-child-lease')`, memberID).Error)
	t.Run("child reuses parent committed lease", func(t *testing.T) {
		child := SetupOryStack(t)
		require.Same(t, parent, child)
		var count int64
		require.NoError(t, child.DB.Table("member").Where("id = ?::uuid", memberID).Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
}

func TestOryIntegrationCleanupJoinsErrorsAndRunsEveryStage(t *testing.T) {
	restoreErr := errors.New("restore")
	identityErr := errors.New("identity")
	pgmqErr := errors.New("pgmq")
	spiceErr := errors.New("spicedb")
	var stages []string
	err := cleanupOryIntegrationTestLease(
		func() error { stages = append(stages, "restore"); return restoreErr },
		func() error { stages = append(stages, "identity"); return identityErr },
		func() error {
			return resetOryIntegrationExternalState(
				func() error { stages = append(stages, "pgmq"); return pgmqErr },
				func() error { stages = append(stages, "spicedb"); return spiceErr },
			)
		},
	)
	require.Equal(t, []string{"restore", "identity", "pgmq", "spicedb"}, stages)
	require.ErrorIs(t, err, restoreErr)
	require.ErrorIs(t, err, identityErr)
	require.ErrorIs(t, err, pgmqErr)
	require.ErrorIs(t, err, spiceErr)
}

func TestIntegrationDatabaseBaselineRequiresOrchestratorLease(t *testing.T) {
	baseline, err := CaptureIntegrationDatabaseBaseline(t.Context(), &AppPostgres{}, &gorm.DB{})
	require.Nil(t, baseline)
	require.ErrorContains(t, err, "orchestrator PostgreSQL lease")
}

func TestOryIntegrationSuiteCloseJoinsErrorsAndRunsEveryCleanupOnce(t *testing.T) {
	firstErr := errors.New("first")
	secondErr := errors.New("second")
	var calls []string
	suite := &OryIntegrationSuite{cleanups: []func() error{
		func() error { calls = append(calls, "first"); return firstErr },
		func() error { calls = append(calls, "second"); return secondErr },
	}}

	err := suite.Close()
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.Equal(t, []string{"second", "first"}, calls)

	require.ErrorIs(t, suite.Close(), firstErr)
	require.ErrorIs(t, suite.Close(), secondErr)
	require.Equal(t, []string{"second", "first"}, calls)
}

func TestOryIntegrationSuiteCloseDrainsSuiteCleanups(t *testing.T) {
	integrationCleanupMu.Lock()
	registeredBefore := integrationRegisteredCleanups
	suiteBefore := integrationSuiteCleanups
	tempRootBefore := integrationSuiteTempRoot
	integrationRegisteredCleanups = nil
	integrationSuiteCleanups = nil
	integrationSuiteTempRoot = ""
	integrationCleanupMu.Unlock()
	defer func() {
		integrationCleanupMu.Lock()
		integrationRegisteredCleanups = registeredBefore
		integrationSuiteCleanups = suiteBefore
		integrationSuiteTempRoot = tempRootBefore
		integrationCleanupMu.Unlock()
	}()

	calls := 0
	integrationCleanupMu.Lock()
	integrationSuiteCleanups = []*integrationCleanup{
		newIntegrationCleanup("runtime temp root", func() error {
			calls++
			return nil
		}),
	}
	integrationCleanupMu.Unlock()

	suite := &OryIntegrationSuite{}
	require.NoError(t, suite.Close())
	require.Equal(t, 1, calls)
}

func setupOryIntegrationSuiteTest(t *testing.T) *OryIntegrationSuite {
	t.Helper()
	suite, err := StartOryIntegrationSuite(t.Context())
	if suite != nil {
		t.Cleanup(func() { require.NoError(t, suite.Close()) })
	}
	require.NoError(t, err)
	return suite
}

func integrationBaselineCounts(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	tables, err := integrationDatabaseTables(t.Context(), db)
	require.NoError(t, err)
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		counts[table] = count
	}
	return counts
}

func assertRequiredIntegrationBaseline(t *testing.T, counts map[string]int64) {
	t.Helper()
	for _, table := range []string{
		`public.country`, `public.site_settings`, `public.map_theme`, `public.email_template`, `public.translation_locale`, `pgmq.meta`,
	} {
		count, ok := counts[table]
		require.Truef(t, ok, "baseline table %s must be captured", table)
		require.Positivef(t, count, "baseline table %s must retain bootstrap data", table)
	}
	kratosTableCount := 0
	for table := range counts {
		if strings.HasPrefix(table, "kratos.") {
			kratosTableCount++
		}
	}
	require.Positive(t, kratosTableCount, "Kratos migration tables must be captured")
}
