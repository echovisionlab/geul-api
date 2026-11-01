//go:build integration

package testutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// RequireKratosLockWaiters waits until want sessions of the test's own
// database wait on a lock. kratos.identities is a postgres_fdw foreign table
// of the shared Kratos database, so a session waiting on an Identity row
// waits here for the remote result (PostgresFdwGetResult) rather than in
// pg_locks; both count. The shared Kratos database itself is not counted:
// its waiters belong to every test that runs at the same time.
func RequireKratosLockWaiters(t *testing.T, db *gorm.DB, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		err := db.Raw(`
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND (wait_event_type = 'Lock' OR wait_event = 'PostgresFdwGetResult')`).Scan(&waiting).Error
		return err == nil && waiting >= want
	}, 10*time.Second, 10*time.Millisecond)
}
