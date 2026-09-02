package sitesettings

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestReferencesValidatePublishedPageAndMenu(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE page (id TEXT PRIMARY KEY, status TEXT NOT NULL)").Error)
	require.NoError(t, db.Exec("CREATE TABLE menu (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO page (id, status) VALUES (?, ?)", "published", managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()).Error)
	require.NoError(t, db.Exec("INSERT INTO page (id, status) VALUES (?, ?)", "draft", managev1.PageStatus_PAGE_STATUS_DRAFT.String()).Error)
	require.NoError(t, db.Exec("INSERT INTO menu (id) VALUES (?)", "menu-id").Error)
	adapter := NewReferences()

	published, draft, menuID, missing := "published", "draft", "menu-id", "missing"
	require.NoError(t, adapter.Validate(t.Context(), db, "homepage_page_id", &published))
	require.Error(t, adapter.Validate(t.Context(), db, "homepage_page_id", &draft))
	require.NoError(t, adapter.Validate(t.Context(), db, "menu_header_id", &menuID))
	require.Error(t, adapter.Validate(t.Context(), db, "menu_footer_id", &missing))
}
