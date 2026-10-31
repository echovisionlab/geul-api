//go:build integration

package transcode

import (
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

// newTranscodeIntegrationDB returns a database owned by the calling test.
func newTranscodeIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	}).DB
}
