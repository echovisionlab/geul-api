//go:build integration

package authentication

import (
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

// newAuthenticationIntegrationDB returns the test's own database with the
// local Kratos stub; nothing is shared with other tests.
func newAuthenticationIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	}).DB
}
