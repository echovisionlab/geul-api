//go:build integration

package transcode

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

var transcodeIntegrationPostgres *testutil.AppPostgres
var transcodeIntegrationPostgresOnce sync.Once
var transcodeIntegrationPostgresCleanup func() error
var transcodeIntegrationPostgresErr error

func TestMain(m *testing.M) {
	code := m.Run()
	if transcodeIntegrationPostgresCleanup != nil {
		if err := transcodeIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup transcode integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedTranscodeIntegrationPostgres() (*testutil.AppPostgres, error) {
	transcodeIntegrationPostgresOnce.Do(func() {
		transcodeIntegrationPostgres, transcodeIntegrationPostgresCleanup, transcodeIntegrationPostgresErr = testutil.StartAppPostgres(context.Background(), testutil.AppPostgresOptions{
			BootstrapKratosStub: true,
			ApplyAppSchemaSQL:   true,
		})
		if transcodeIntegrationPostgresErr != nil {
			fmt.Fprintf(os.Stderr, "start transcode integration postgres: %v\n", transcodeIntegrationPostgresErr)
		}
	})
	return transcodeIntegrationPostgres, transcodeIntegrationPostgresErr
}

func newTranscodeIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	pg, err := sharedTranscodeIntegrationPostgres()
	require.NoError(t, err)
	tx := pg.DB.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
	})
	return tx
}
