//go:build integration

package geoipadapter

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"gorm.io/gorm"
)

// newAdapterGeoIPIntegrationDB returns a database owned by the calling test.
func newAdapterGeoIPIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	}).DB
}
