//go:build integration

package orytest

import (
	"testing"

	pgtest "github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestOryIntegrationSuiteGivesEveryTestItsOwnPostgresAndSpiceDB(t *testing.T) {
	t.Parallel()
	suite := setupOryIntegrationSuiteTest(t)

	const fixedMemberID = "00000000-0000-4000-8000-000000000111"
	var identityID string
	var first *OryStack
	t.Run("writes committed state", func(t *testing.T) {
		first = prepareSuiteStack(t, suite)
		user := first.CreateUser(t, policyv1.Role.Author().ID())
		identityID = user.IdentityID
		require.NoError(t, first.DB.Exec(`
			INSERT INTO member (id, nickname, onboarded, primary_email, available_emails)
			VALUES (?::uuid, 'fixed-isolated-member', TRUE, 'fixed-isolated@example.test', ARRAY['fixed-isolated@example.test']::text[])
		`, fixedMemberID).Error)
	})
	t.Run("sees only shared Kratos", func(t *testing.T) {
		stack := prepareSuiteStack(t, suite)
		require.NotSame(t, first, stack)
		var memberCount, identityCount int64
		require.NoError(t, stack.DB.Table("member").Where("id = ?::uuid", fixedMemberID).Count(&memberCount).Error)
		require.Zero(t, memberCount)
		require.NoError(t, stack.DB.Table("kratos.identities").Where("id = ?::uuid", identityID).Count(&identityCount).Error)
		require.EqualValues(t, 1, identityCount)

		subjects, err := stack.SpiceDBClient.LookupGlobalSubjects(t.Context(), policyv1.Platform.LookupAuthorSubjects())
		require.NoError(t, err)
		require.Empty(t, subjects)
	})
}

func TestIntegrationSuiteFixedIdentifiersCanBeReusedAcrossTests(t *testing.T) {
	t.Parallel()
	suite := setupOryIntegrationSuiteTest(t)

	fixedID := uuid.NewString()
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stack := prepareSuiteStack(t, suite)
			require.NoError(t, stack.DB.Exec(`INSERT INTO member (id, nickname) VALUES (?::uuid, 'repeatable')`, fixedID).Error)
		})
	}
}

func TestOryIntegrationSuiteClonesCarryBootstrapRowsAndKratosTables(t *testing.T) {
	t.Parallel()
	suite := setupOryIntegrationSuiteTest(t)
	stack := prepareSuiteStack(t, suite)

	for _, table := range []string{"country", "site_settings", "map_theme", "email_template", "translation_locale", "pgmq.meta"} {
		var count int64
		require.NoError(t, stack.DB.Table(table).Count(&count).Error)
		require.Positivef(t, count, "clone of %s must retain bootstrap data", table)
	}
	var kratosTables int64
	require.NoError(t, stack.DB.Raw(
		`SELECT count(*) FROM information_schema.foreign_tables WHERE foreign_table_schema = 'kratos'`,
	).Scan(&kratosTables).Error)
	require.Positive(t, kratosTables, "the kratos schema must reach the shared Kratos database")
}

func TestOryIntegrationSuiteSeparateConnectionsShareTheTestDatabase(t *testing.T) {
	t.Parallel()
	suite := setupOryIntegrationSuiteTest(t)
	stack := prepareSuiteStack(t, suite)

	committed, err := gorm.Open(postgres.Open(stack.PostgresDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	committedSQL, err := committed.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, committedSQL.Close()) })
	memberID := uuid.NewString()
	require.NoError(t, committed.Exec(`INSERT INTO member (id, nickname) VALUES (?::uuid, 'separate-connection')`, memberID).Error)
	var count int64
	require.NoError(t, stack.DB.Table("member").Where("id = ?::uuid", memberID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestOryIntegrationSuiteParentAndChildShareLease(t *testing.T) {
	t.Parallel()
	suite := setupOryIntegrationSuiteTest(t)

	const memberID = "00000000-0000-4000-8000-000000000333"
	parent := prepareSuiteStack(t, suite)
	require.NoError(t, parent.DB.Exec(`INSERT INTO member (id, nickname) VALUES (?::uuid, 'parent-child-lease')`, memberID).Error)
	t.Run("child reuses parent committed lease", func(t *testing.T) {
		child := prepareSuiteStack(t, suite)
		require.Same(t, parent, child)
		var count int64
		require.NoError(t, child.DB.Table("member").Where("id = ?::uuid", memberID).Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
}

func TestOryIntegrationSuiteCloseDrainsSuiteCleanups(t *testing.T) {
	t.Parallel()
	calls := 0
	pgtest.WithIntegrationSuiteCleanupRegistration(func() {
		pgtest.RegisterIntegrationCleanup(t, "suite cleanup", func() error {
			calls++
			return nil
		})
	})
	suite := &OryIntegrationSuite{}
	require.NoError(t, suite.Close())
	require.Equal(t, 1, calls)
}

func prepareSuiteStack(t *testing.T, suite *OryIntegrationSuite) *OryStack {
	t.Helper()
	return suite.prepareTest(t)
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
