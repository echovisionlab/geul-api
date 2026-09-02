package postgreslock

import (
	"context"
	"database/sql"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReleaseDoubleFailureDiscardsReservedConnection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	var reserved *sql.Conn
	require.NoError(t, db.Connection(func(connection *gorm.DB) error {
		var ok bool
		reserved, ok = connection.Statement.ConnPool.(*sql.Conn)
		require.True(t, ok)
		require.NotNil(t, reserved)

		err := Release(context.Background(), connection, 123, "test")
		require.ErrorContains(t, err, "release test advisory lock")
		require.ErrorContains(t, err, "release all advisory locks")
		return nil
	}))

	require.ErrorIs(t, reserved.PingContext(context.Background()), sql.ErrConnDone)
}
