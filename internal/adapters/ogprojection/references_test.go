package ogprojection_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/adapters/ogprojection"
)

func TestAssetReferencesCoverEveryOGPointerTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pointers := []struct {
		table  string
		column string
	}{
		{table: "post", column: "og_asset_id"},
		{table: "page", column: "og_asset_id"},
		{table: "form", column: "og_asset_id"},
		{table: "label", column: "og_asset_id"},
		{table: "release", column: "og_asset_id"},
		{table: "series_translation", column: "og_asset_id"},
		{table: "post_translation", column: "og_asset_id"},
		{table: "page_translation", column: "og_asset_id"},
		{table: "form_translation", column: "og_asset_id"},
		{table: "work_translation", column: "og_asset_id"},
		{table: "artist_translation", column: "og_asset_id"},
		{table: "site_settings", column: "site_og_asset_id"},
	}
	for i, pointer := range pointers {
		require.NoError(t, db.Exec(fmt.Sprintf("CREATE TABLE %s (id text PRIMARY KEY, %s text)", pointer.table, pointer.column)).Error)
		require.NoError(t, db.Exec(
			fmt.Sprintf("INSERT INTO %s (id, %s) VALUES (?, ?)", pointer.table, pointer.column),
			fmt.Sprintf("owner-%d", i), "asset-"+pointer.table,
		).Error)
	}
	references := ogprojection.AssetReferences()
	referencedBy := func(assetID string) bool {
		for _, reference := range references {
			referenced, err := reference.ReferencesAsset(t.Context(), db, assetID)
			require.NoError(t, err)
			if referenced {
				return true
			}
		}
		return false
	}

	for _, pointer := range pointers {
		require.True(t, referencedBy("asset-"+pointer.table), "%s.%s keeps its asset", pointer.table, pointer.column)
	}
	require.False(t, referencedBy("asset-unreferenced"))
}
