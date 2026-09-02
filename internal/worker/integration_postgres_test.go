//go:build integration

package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

var workerIntegrationPostgres *testutil.AppPostgres
var workerIntegrationPostgresOnce sync.Once
var workerIntegrationPostgresCleanup func() error
var workerIntegrationPostgresErr error

func TestMain(m *testing.M) {
	code := m.Run()
	if workerIntegrationPostgresCleanup != nil {
		if err := workerIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup worker integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedWorkerIntegrationPostgres() (*testutil.AppPostgres, error) {
	workerIntegrationPostgresOnce.Do(func() {
		workerIntegrationPostgres, workerIntegrationPostgresCleanup, workerIntegrationPostgresErr = testutil.StartAppPostgres(context.Background(), testutil.AppPostgresOptions{
			BootstrapKratosStub: true,
			ApplyAppSchemaSQL:   true,
		})
		if workerIntegrationPostgresErr != nil {
			fmt.Fprintf(os.Stderr, "start worker integration postgres: %v\n", workerIntegrationPostgresErr)
		}
	})
	return workerIntegrationPostgres, workerIntegrationPostgresErr
}

func newWorkerIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	pg, err := sharedWorkerIntegrationPostgres()
	require.NoError(t, err)
	tx := pg.DB.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
	})
	return tx
}

// newCommittedWorkerIntegrationDB is reserved for code paths that own a real
// top-level transaction and therefore cannot run inside the rollback fixture.
// The exact migrated database baseline is restored after the test instead.
func newCommittedWorkerIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	pg, err := sharedWorkerIntegrationPostgres()
	require.NoError(t, err)
	baseline, err := testutil.CaptureIntegrationDatabaseBaseline(t.Context(), pg, pg.DB)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, baseline.Close())
	})
	t.Cleanup(func() {
		require.NoError(t, baseline.Restore(context.Background()))
	})
	return pg.DB
}
