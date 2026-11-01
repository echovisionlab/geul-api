package filemediapostgres

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIngestAdvisoryKeysKeepTheExistingDeploymentIdentity(t *testing.T) {
	t.Parallel()
	require.Equal(t, int64(-5041752992068716055), ingestLockKey(" file-id "))
	require.Equal(t, int64(6166175492533980234), ingestLockKey("multipart-completion:upload-id\x00file-id"))
}

func TestIngestAdvisoryFallbackKeepsTheCallbackError(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	locks := NewIngestLocks(db, Ports{})
	failure := errors.New("ingest failure")
	calls := 0
	require.ErrorIs(t, locks.RemoteImport(t.Context(), "file", func(session domain.IngestSession) error {
		calls++
		require.NotNil(t, session.Units)
		require.NotNil(t, session.FaviconCommits)
		return failure
	}), failure)
	require.Equal(t, 1, calls)
	require.ErrorContains(t, locks.RemoteImport(t.Context(), "file", nil), "operation is required")
	require.ErrorContains(t, locks.Multipart(t.Context(), "upload", "file", nil), "operation is required")
}
