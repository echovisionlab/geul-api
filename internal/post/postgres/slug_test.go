package postpostgres

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStoreReportsPostAndPageRouteSlugs(t *testing.T) {
	db := newSlugTestDB(t)
	require.NoError(t, db.Exec("INSERT INTO post (id, slug) VALUES (?, ?)", "post-a", "taken").Error)
	require.NoError(t, db.Exec("INSERT INTO page (slug) VALUES (?)", "posts/page-owned").Error)

	store := Bind(db, Deps{})
	tests := []struct {
		name          string
		slug          string
		excludePostID string
		taken         bool
		routeFree     bool
	}{
		{name: "unclaimed", slug: "available", routeFree: true},
		{name: "claimed by post", slug: "taken", taken: true, routeFree: true},
		{name: "current post excluded", slug: "taken", excludePostID: "post-a", routeFree: true},
		{name: "claimed by page route", slug: "page-owned"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			taken, err := store.SlugTaken(t.Context(), test.slug, test.excludePostID)
			require.NoError(t, err)
			require.Equal(t, test.taken, taken)
			routeFree, err := store.RouteAvailable(t.Context(), test.slug)
			require.NoError(t, err)
			require.Equal(t, test.routeFree, routeFree)
		})
	}
}

func newSlugTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE post (id TEXT PRIMARY KEY, slug TEXT)").Error)
	require.NoError(t, db.Exec("CREATE TABLE page (slug TEXT)").Error)
	return db
}
