//go:build integration

package mq

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/stretchr/testify/require"
)

// newMQIntegrationPostgres owns a disposable logical database per test, so queue
// purge and committed transaction assertions never affect another test's queues.
func newMQIntegrationPostgres(t *testing.T, queues ...string) *pgtest.AppPostgres {
	t.Helper()
	pg := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	for _, queue := range queues {
		require.NoError(t, pgtest.PurgePGMQQueue(t.Context(), pg.SQLDB, queue))
	}
	return pg
}
