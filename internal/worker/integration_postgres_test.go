//go:build integration

package worker

import (
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

// newWorkerIntegrationPostgres returns a database owned by the calling test.
func newWorkerIntegrationPostgres(t *testing.T) *testutil.AppPostgres {
	t.Helper()

	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
}

func newWorkerIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	return newWorkerIntegrationPostgres(t).DB
}

// newCommittedWorkerIntegrationDB is used by code paths that own a real
// top-level transaction. The database belongs to the test, so committed rows
// never reach another test.
func newCommittedWorkerIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	return newWorkerIntegrationPostgres(t).DB
}
