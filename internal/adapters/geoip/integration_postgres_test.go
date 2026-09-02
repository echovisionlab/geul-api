//go:build integration

package geoipadapter

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var adapterGeoIPIntegrationPostgres *testutil.AppPostgres
var adapterGeoIPIntegrationPostgresOnce sync.Once
var adapterGeoIPIntegrationPostgresCleanup func() error
var adapterGeoIPIntegrationPostgresErr error

func TestMain(m *testing.M) {
	code := m.Run()
	if adapterGeoIPIntegrationPostgresCleanup != nil {
		if err := adapterGeoIPIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup GeoIP adapter integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedAdapterGeoIPIntegrationPostgres() (*testutil.AppPostgres, error) {
	adapterGeoIPIntegrationPostgresOnce.Do(func() {
		adapterGeoIPIntegrationPostgres, adapterGeoIPIntegrationPostgresCleanup, adapterGeoIPIntegrationPostgresErr = testutil.StartAppPostgres(
			context.Background(),
			testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true},
		)
	})
	return adapterGeoIPIntegrationPostgres, adapterGeoIPIntegrationPostgresErr
}

func newAdapterGeoIPIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	postgres, err := sharedAdapterGeoIPIntegrationPostgres()
	require.NoError(t, err)
	tx := postgres.DB.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
	})
	return tx
}
