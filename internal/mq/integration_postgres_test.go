//go:build integration

package mq

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

var mqIntegrationPostgres *testutil.AppPostgres
var mqIntegrationPostgresOnce sync.Once
var mqIntegrationPostgresCleanup func() error
var mqIntegrationPostgresErr error

func TestMain(m *testing.M) {
	code := m.Run()
	if mqIntegrationPostgresCleanup != nil {
		if err := mqIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup mq integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedMQIntegrationPostgres() (*testutil.AppPostgres, error) {
	mqIntegrationPostgresOnce.Do(func() {
		mqIntegrationPostgres, mqIntegrationPostgresCleanup, mqIntegrationPostgresErr = testutil.StartAppPostgres(context.Background(), testutil.AppPostgresOptions{
			BootstrapKratosStub: true,
			ApplyAppSchemaSQL:   true,
		})
		if mqIntegrationPostgresErr != nil {
			fmt.Fprintf(os.Stderr, "start mq integration postgres: %v\n", mqIntegrationPostgresErr)
		}
	})
	return mqIntegrationPostgres, mqIntegrationPostgresErr
}

// resetMQIntegrationQueues gives each test an empty queue before it starts and
// after it finishes. The tests intentionally keep their explicit SQL
// transactions so commit/rollback visibility remains part of the assertion.
func resetMQIntegrationQueues(t *testing.T, queues ...string) *testutil.AppPostgres {
	t.Helper()

	pg, err := sharedMQIntegrationPostgres()
	require.NoError(t, err)
	for _, queue := range queues {
		require.NoError(t, testutil.PurgePGMQQueue(t.Context(), pg.SQLDB, queue))
	}
	t.Cleanup(func() {
		for _, queue := range queues {
			require.NoError(t, testutil.PurgePGMQQueue(context.Background(), pg.SQLDB, queue))
		}
	})
	return pg
}
