package post

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCheckSlugAvailable(t *testing.T) {
	db := newSlugTestDB(t)
	require.NoError(t, db.Exec("INSERT INTO post (id, slug) VALUES (?, ?)", "post-a", "taken").Error)
	require.NoError(t, db.Exec("INSERT INTO page (slug) VALUES (?)", "posts/page-owned").Error)

	tests := []struct {
		name          string
		slug          string
		excludePostID string
		available     bool
		wantErr       bool
		errorCode     connect.Code
	}{
		{name: "unclaimed", slug: "available", available: true},
		{name: "claimed by post", slug: "taken", available: false},
		{name: "current post excluded", slug: "taken", excludePostID: "post-a", available: true},
		{name: "claimed by page route", slug: "page-owned", available: false},
		{name: "slash rejected", slug: "not/valid", wantErr: true, errorCode: connect.CodeInvalidArgument},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			available, err := CheckSlugAvailable(t.Context(), db, test.slug, test.excludePostID)
			if test.wantErr {
				require.Equal(t, test.errorCode, connect.CodeOf(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.available, available)
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
