//go:build integration

package scheduler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresLeaderTransfersLockAcrossTwoConnectionsIntegration(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{})
	postgres.SQLDB.SetMaxOpenConns(4)

	firstConn, err := postgres.SQLDB.Conn(t.Context())
	require.NoError(t, err)
	secondConn, err := postgres.SQLDB.Conn(t.Context())
	require.NoError(t, err)

	firstSession := &sqlAdvisoryLockSession{conn: firstConn}
	secondSession := &sqlAdvisoryLockSession{conn: secondConn}
	firstLeader := testPostgresLeader(firstSession)
	firstLeader.instanceID = "instance-a"
	secondLeader := testPostgresLeader(secondSession)
	secondLeader.instanceID = "instance-b"
	firstReleased := false
	secondReleased := false
	t.Cleanup(func() {
		if !firstReleased {
			firstLeader.releaseSession(firstSession)
		}
		if !secondReleased {
			secondLeader.releaseSession(secondSession)
		}
	})

	require.NoError(t, firstLeader.refreshLeadership(t.Context(), firstSession))
	require.True(t, firstLeader.IsLeader())

	require.NoError(t, secondLeader.refreshLeadership(t.Context(), secondSession))
	require.False(t, secondLeader.IsLeader())

	firstLeader.releaseSession(firstSession)
	firstReleased = true
	require.False(t, firstLeader.IsLeader())

	require.NoError(t, secondLeader.refreshLeadership(t.Context(), secondSession))
	require.True(t, secondLeader.IsLeader())

	secondLeader.releaseSession(secondSession)
	secondReleased = true
	require.False(t, secondLeader.IsLeader())
}
