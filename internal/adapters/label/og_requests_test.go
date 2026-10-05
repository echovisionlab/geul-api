package label

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestRequestsPreferLightLogoThenDark(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE label (
		id text PRIMARY KEY, source_locale text NOT NULL, logo_light_file_id text, logo_dark_file_id text
	);
	CREATE TABLE label_translation (
		entity_id text, locale text, title text, PRIMARY KEY (entity_id, locale)
	)`).Error)
	lightID, darkID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO label (id, source_locale, logo_light_file_id, logo_dark_file_id) VALUES (?, 'en', ?, ?), (?, 'en', NULL, ?), (?, 'en', NULL, NULL)",
		"both", lightID, darkID, "dark-only", darkID, "none",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO label_translation (entity_id, locale, title) VALUES (?, 'en', 'Both'), (?, 'en', 'Dark'), (?, 'en', 'None')",
		"both", "dark-only", "none",
	).Error)
	selection := &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
		Primary: &managev1.OgPrimaryTarget{},
	}}

	both, err := NewRequests().Resolve(t.Context(), db, "label", "both", selection)
	require.NoError(t, err)
	require.Len(t, both, 1)
	require.NotNil(t, both[0].FeaturedImageFileID)
	assert.Equal(t, lightID, *both[0].FeaturedImageFileID)

	darkOnly, err := NewRequests().Resolve(t.Context(), db, "label", "dark-only", selection)
	require.NoError(t, err)
	require.Len(t, darkOnly, 1)
	require.NotNil(t, darkOnly[0].FeaturedImageFileID)
	assert.Equal(t, darkID, *darkOnly[0].FeaturedImageFileID)

	_, err = NewRequests().Resolve(t.Context(), db, "label", "none", selection)
	require.Error(t, err)
}
