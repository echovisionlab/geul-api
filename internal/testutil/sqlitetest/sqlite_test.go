package sqlitetest

import (
	"database/sql"
	"testing"

	puresqlite "github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestOpenIsolatesInvocationsAndSharesOnlyItsOwnConnections(t *testing.T) {
	t.Parallel()
	for name, driver := range map[string]func(string) gorm.Dialector{
		"sqlite": sqlite.Open,
		"pure":   puresqlite.Open,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, err := Open(t, driver, &gorm.Config{})
			require.NoError(t, err)
			second, err := Open(t, driver, &gorm.Config{})
			require.NoError(t, err)
			for _, db := range []*gorm.DB{first, second} {
				require.NoError(t, db.Exec("CREATE TABLE fixture (id INTEGER PRIMARY KEY)").Error)
			}
			require.NoError(t, first.Exec("INSERT INTO fixture VALUES (1)").Error)
			var count int64
			require.NoError(t, second.Table("fixture").Count(&count).Error)
			require.Zero(t, count, "another invocation must not share fixture data")

			pool, err := first.DB()
			require.NoError(t, err)
			one, err := pool.Conn(t.Context())
			require.NoError(t, err)
			defer one.Close()
			two, err := pool.Conn(t.Context())
			require.NoError(t, err)
			defer two.Close()
			require.NoError(t, two.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM fixture").Scan(&count))
			require.EqualValues(t, 1, count, "a fixture's pool connections must share its schema and rows")
		})
	}
}

func TestOpenClosesPoolAfterOwnerFinishes(t *testing.T) {
	t.Parallel()
	var pool *sql.DB
	t.Run("owner", func(t *testing.T) {
		db, err := Open(t, sqlite.Open, &gorm.Config{})
		require.NoError(t, err)
		pool, err = db.DB()
		require.NoError(t, err)
		require.NoError(t, pool.Ping())
	})
	require.Error(t, pool.Ping(), "fixture pool must not leak beyond its owner")
}
