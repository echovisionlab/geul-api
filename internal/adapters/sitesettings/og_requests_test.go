package sitesettings

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestRenderConfigSnapshotRejectsCorruptStoredJSON(t *testing.T) {
	db := siteOGUnitDB(t)
	require.NoError(t, db.Exec("UPDATE site_settings SET og_image_config = ? WHERE id = 1", []byte(`{"invalid"`)).Error)

	_, _, err := NewRenderConfig().Snapshot(t.Context(), db, "https://cdn.example.com")
	require.ErrorContains(t, err, "not valid JSON")
}

func TestRequestsUseCanonicalSettingsRowBackground(t *testing.T) {
	db := siteOGUnitDB(t)
	fileID := uuid.NewString()
	require.NoError(t, db.Exec("UPDATE site_settings SET site_og_background_file_id = ? WHERE id = 1", fileID).Error)

	requests, err := NewRequests().Resolve(t.Context(), db, "site", "default", primarySelection())
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.NotNil(t, requests[0].FeaturedImageFileID)
	assert.Equal(t, fileID, *requests[0].FeaturedImageFileID)
}

func siteOGUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE site_settings (
		id integer PRIMARY KEY, site_title text NOT NULL DEFAULT '',
		primary_color text NOT NULL DEFAULT '#b02d23', logo_light_file_id text,
		site_og_background_file_id text, og_image_config blob
	)`).Error)
	require.NoError(t, db.Exec("INSERT INTO site_settings (id) VALUES (1)").Error)
	return db
}

func primarySelection() *managev1.OgTargetSelection {
	return &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
		Primary: &managev1.OgPrimaryTarget{},
	}}
}
