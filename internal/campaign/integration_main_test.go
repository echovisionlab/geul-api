//go:build integration

package campaign

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Campaign integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "cleanup Campaign integration runtime: %v\n", err)
		code = 1
	}
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Campaign integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newCampaignConcurrentIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	return openCampaignIntegrationDB(t, stack)
}

// newCampaignLocalKratosStack is for tests that write Campaign blocks or freeze
// excluded Members. Those paths lock member JOIN kratos.identities FOR SHARE;
// through postgres_fdw that becomes a REPEATABLE READ lock on every active
// identity in the shared Kratos, so identity updates by concurrent tests fail
// it with serialization errors.
func newCampaignLocalKratosStack(t *testing.T) *testutil.OryStack {
	t.Helper()
	return testutil.SetupOryStackWithLocalKratos(t)
}

func openCampaignIntegrationDB(t *testing.T, stack *testutil.OryStack) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}
