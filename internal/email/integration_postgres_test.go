//go:build integration

package email

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

var emailIntegrationPostgres *testutil.AppPostgres
var emailIntegrationPostgresOnce sync.Once
var emailIntegrationPostgresCleanup func() error
var emailIntegrationPostgresErr error

func TestMain(m *testing.M) {
	code := m.Run()
	if emailIntegrationPostgresCleanup != nil {
		if err := emailIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup email integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedEmailIntegrationPostgres() (*testutil.AppPostgres, error) {
	emailIntegrationPostgresOnce.Do(func() {
		emailIntegrationPostgres, emailIntegrationPostgresCleanup, emailIntegrationPostgresErr = testutil.StartAppPostgres(context.Background(), testutil.AppPostgresOptions{
			BootstrapKratosStub: true,
			ApplyAppSchemaSQL:   true,
		})
		if emailIntegrationPostgresErr != nil {
			fmt.Fprintf(os.Stderr, "start email integration postgres: %v\n", emailIntegrationPostgresErr)
		}
	})
	return emailIntegrationPostgres, emailIntegrationPostgresErr
}

func newEmailIntegrationTransaction(t *testing.T) *gorm.DB {
	t.Helper()

	pg, err := sharedEmailIntegrationPostgres()
	require.NoError(t, err)
	tx := pg.DB.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
	})
	return tx
}
