package menupostgres

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLockTargetsFindsConcreteRowsByIDOrSlug(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, table := range []string{"page", "category", "tag", "series"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id TEXT PRIMARY KEY, slug TEXT)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" (id, slug) VALUES (?, ?)", table+"-id", table+"-slug").Error)
	}
	targets := NewStore(db, Ports{})
	refs := []menu.TargetReference{
		{LinkType: menu.LinkPage, ID: "page-id"},
		{LinkType: menu.LinkCategory, Slug: "category-slug"},
		{LinkType: menu.LinkTag, ID: "tag-id"},
		{LinkType: menu.LinkSeries, Slug: "series-slug"},
	}
	require.NoError(t, targets.LockTargets(t.Context(), refs))

	refs[0].ID = "missing"
	require.ErrorIs(t, targets.LockTargets(t.Context(), refs), menu.TargetNotFoundError{LinkType: menu.LinkPage})
}
