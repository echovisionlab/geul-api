//go:build integration

package member

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start member Ory integration suite:", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if closeErr := suite.Close(); closeErr != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "cleanup member Ory integration suite:", closeErr)
		code = 1
	}
	os.Exit(code)
}

func newConcurrentServiceIntegrationPostgres(t *testing.T) *testutil.AppPostgres {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	return &testutil.AppPostgres{DSN: stack.PostgresDSN, SQLDB: sqlDB, DB: stack.DB}
}

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}
