//go:build integration

package geoip_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

var geoIPIntegrationPostgres *testutil.AppPostgres
var geoIPIntegrationPostgresOnce sync.Once
var geoIPIntegrationPostgresCleanup func() error
var geoIPIntegrationPostgresErr error

func TestMain(m *testing.M) {
	code := m.Run()
	if geoIPIntegrationPostgresCleanup != nil {
		if err := geoIPIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup geoip integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedGeoIPIntegrationPostgres() (*testutil.AppPostgres, error) {
	geoIPIntegrationPostgresOnce.Do(func() {
		geoIPIntegrationPostgres, geoIPIntegrationPostgresCleanup, geoIPIntegrationPostgresErr = testutil.StartAppPostgres(context.Background(), testutil.AppPostgresOptions{
			BootstrapKratosStub: true,
			ApplyAppSchemaSQL:   true,
		})
		if geoIPIntegrationPostgresErr != nil {
			fmt.Fprintf(os.Stderr, "start geoip integration postgres: %v\n", geoIPIntegrationPostgresErr)
		}
	})
	return geoIPIntegrationPostgres, geoIPIntegrationPostgresErr
}

func newGeoIPIntegrationTransaction(t *testing.T) *gorm.DB {
	t.Helper()

	pg, err := sharedGeoIPIntegrationPostgres()
	require.NoError(t, err)
	tx := pg.DB.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
	})
	return tx
}
