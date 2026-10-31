//go:build integration

package testutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// RequireKratosLockWaiters waits until want sessions of the test's Kratos
// database are blocked on a lock. kratos.identities is a postgres_fdw foreign
// table, so a lock of an Identity row is awaited by the remote session in the
// Kratos database it points at (as a cursor FETCH), not by a session of the
// test's own database that RequireLockWaiters watches.
func RequireKratosLockWaiters(t *testing.T, db *gorm.DB, want int) {
	t.Helper()
	var kratosDatabase string
	require.NoError(t, db.Raw(`
		SELECT option.option_value
		FROM pg_foreign_table AS foreign_table
		JOIN pg_class AS class ON class.oid = foreign_table.ftrelid
		JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
		JOIN pg_foreign_server AS server ON server.oid = foreign_table.ftserver
		CROSS JOIN LATERAL pg_options_to_table(server.srvoptions) AS option
		WHERE namespace.nspname = 'kratos' AND class.relname = 'identities' AND option.option_name = 'dbname'`,
	).Scan(&kratosDatabase).Error)
	require.NotEmpty(t, kratosDatabase, "kratos.identities is not a foreign table")
	require.Eventually(t, func() bool {
		var waiting int
		err := db.Raw(`
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = ? AND wait_event_type = 'Lock'`, kratosDatabase).Scan(&waiting).Error
		return err == nil && waiting >= want
	}, 10*time.Second, 10*time.Millisecond)
}
