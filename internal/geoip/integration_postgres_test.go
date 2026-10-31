//go:build integration

package geoip_test

import (
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

// newGeoIPIntegrationTransaction returns a database owned by the calling test.
func newGeoIPIntegrationTransaction(t *testing.T) *gorm.DB {
	t.Helper()

	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	}).DB
}
