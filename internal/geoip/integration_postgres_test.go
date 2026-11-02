//go:build integration

package geoip_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"gorm.io/gorm"
)

// newGeoIPIntegrationTransaction returns a database owned by the calling test.
func newGeoIPIntegrationTransaction(t *testing.T) *gorm.DB {
	t.Helper()

	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	}).DB
}
