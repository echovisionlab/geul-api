package sitesettings

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMCPServerTitleSourceReadsCurrentCanonicalSiteTitle(t *testing.T) {
	t.Parallel()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE site_settings (id INTEGER PRIMARY KEY, site_title TEXT NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO site_settings (id, site_title) VALUES (1, ' Geul Studio ')").Error)

	source := NewMCPServerTitleSource(db)
	title, err := source.ServerTitle(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Geul Studio", title)

	require.NoError(t, db.Exec("UPDATE site_settings SET site_title = 'Second Title' WHERE id = 1").Error)
	title, err = source.ServerTitle(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Second Title", title)
}
