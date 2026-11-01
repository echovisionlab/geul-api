//go:build integration

package worker

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"gorm.io/gorm"
)

// newWorkerIntegrationPostgres returns a database owned by the calling test.
func newWorkerIntegrationPostgres(t *testing.T) *pgtest.AppPostgres {
	t.Helper()

	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
}

func newWorkerIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	return newWorkerIntegrationPostgres(t).DB
}
