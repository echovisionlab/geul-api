//go:build integration

package authentication

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
)

var authenticationIntegrationPostgres *testutil.AppPostgres
var authenticationIntegrationCleanup func() error
var authenticationIntegrationErr error
var authenticationIntegrationOnce sync.Once

func TestMain(m *testing.M) {
	code := m.Run()
	if authenticationIntegrationCleanup != nil {
		if err := authenticationIntegrationCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup authentication Postgres integration: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedAuthenticationIntegrationPostgres() (*testutil.AppPostgres, error) {
	authenticationIntegrationOnce.Do(func() {
		authenticationIntegrationPostgres, authenticationIntegrationCleanup, authenticationIntegrationErr = testutil.StartAppPostgres(context.Background(), testutil.AppPostgresOptions{
			BootstrapKratosStub: true,
			ApplyAppSchemaSQL:   true,
		})
	})
	return authenticationIntegrationPostgres, authenticationIntegrationErr
}

func newAuthenticationIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	pg, err := sharedAuthenticationIntegrationPostgres()
	require.NoError(t, err)
	return pg.DB
}

func newConcurrentAuthenticationIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	pg, err := sharedAuthenticationIntegrationPostgres()
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.Open(pg.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}
