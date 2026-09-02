package menuadapter

import (
	"testing"

	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTargetReferencesValidateConcreteRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, table := range []string{"page", "category", "tag", "series"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id TEXT PRIMARY KEY, slug TEXT)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" (id, slug) VALUES (?, ?)", table+"-id", table+"-slug").Error)
	}
	adapter := NewTargetReferences()
	refs := []menudomain.TargetReference{
		{LinkType: managev1.MenuLinkType_MENU_LINK_TYPE_PAGE, ID: "page-id"},
		{LinkType: managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY, Slug: "category-slug"},
		{LinkType: managev1.MenuLinkType_MENU_LINK_TYPE_TAG, ID: "tag-id"},
		{LinkType: managev1.MenuLinkType_MENU_LINK_TYPE_SERIES, Slug: "series-slug"},
	}
	require.NoError(t, adapter.ValidateAndLock(t.Context(), db, refs))

	refs[0].ID = "missing"
	require.ErrorContains(t, adapter.ValidateAndLock(t.Context(), db, refs), "page target does not exist")
}
