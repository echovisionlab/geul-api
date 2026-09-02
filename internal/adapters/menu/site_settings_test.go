package menuadapter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSiteSettingsReferencesClearsOnlyMatchingMenuSlots(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:menu-site-settings?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE site_settings (
		id INTEGER PRIMARY KEY,
		menu_header_id TEXT,
		menu_secondary_id TEXT,
		menu_footer_id TEXT,
		menu_avatar_dropdown_id TEXT,
		updated_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO site_settings (
		id, menu_header_id, menu_secondary_id, menu_footer_id, menu_avatar_dropdown_id
	) VALUES (1, 'menu-1', 'menu-2', 'menu-1', NULL)`).Error)

	adapter := NewSiteSettingsReferences(nil)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return adapter.ClearMenuReferences(context.Background(), tx, "menu-1")
	}))

	var slots struct {
		Header    *string `gorm:"column:menu_header_id"`
		Secondary *string `gorm:"column:menu_secondary_id"`
		Footer    *string `gorm:"column:menu_footer_id"`
		Avatar    *string `gorm:"column:menu_avatar_dropdown_id"`
	}
	require.NoError(t, db.Table("site_settings").Where("id = ?", 1).Scan(&slots).Error)
	require.Nil(t, slots.Header)
	require.NotNil(t, slots.Secondary)
	require.Equal(t, "menu-2", *slots.Secondary)
	require.Nil(t, slots.Footer)
	require.Nil(t, slots.Avatar)
}
