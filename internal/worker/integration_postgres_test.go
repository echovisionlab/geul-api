//go:build integration

package worker

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

// newWorkerIntegrationPostgres returns a database owned by the calling test.
func newWorkerIntegrationPostgres(t *testing.T) *pgtest.AppPostgres {
	t.Helper()

	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
}
