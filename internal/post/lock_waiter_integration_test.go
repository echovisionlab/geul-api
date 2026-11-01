//go:build integration

package post_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// requireMutationWaiting waits until want transactions of db's database wait
// for a lock another holds; result fails the test when a mutation returned
// without waiting. The Kratos tables of a test database are postgres_fdw
// tables of the shared Kratos database, so a wait on an Identity row shows
// as a backend waiting for the remote result, not in pg_locks.
func requireMutationWaiting(t *testing.T, db *gorm.DB, want int, result <-chan error) {
	t.Helper()
	waiting := func() int {
		var count int
		require.NoError(t, db.Raw(`SELECT
			(SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			 WHERE NOT l.granted AND a.datname = current_database())
			+ (SELECT count(*) FROM pg_stat_activity
			   WHERE datname = current_database() AND wait_event = 'PostgresFdwGetResult')`).Scan(&count).Error)
		return count
	}
	deadline := time.Now().Add(10 * time.Second)
	for waiting() < want {
		select {
		case err := <-result:
			t.Fatalf("the mutation returned before waiting for the held lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the mutation never waited for the held lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
