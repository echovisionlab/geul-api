package work

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestOGResolverMapsMissingWorkToNotFound(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE work (id TEXT PRIMARY KEY, source_locale TEXT, featured_image_file_id TEXT);
		CREATE TABLE work_translation (
			entity_id TEXT, locale TEXT, title TEXT
		)
	`).Error)
	missingID := uuid.NewString()
	_, err = og.NewResolver(NewRequests()).Resolve(t.Context(), db, &managev1.RegenerateOgImageRequest{
		EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
		EntityId:   &missingID,
		Selection: &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
			Primary: &managev1.OgPrimaryTarget{},
		}},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestOGRequestsResolveExistingTargetValue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE work (id TEXT PRIMARY KEY, source_locale TEXT, featured_image_file_id TEXT);
		CREATE TABLE work_translation (
			entity_id TEXT, locale TEXT, title TEXT
		)
	`).Error)
	workID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO work (id, source_locale) VALUES (?, 'en')`, workID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO work_translation (
			entity_id, locale, title
		) VALUES
			(?, 'en', 'Source title'),
			(?, 'ko', 'Stored Korean title')
	`, workID, workID).Error)

	requests, err := NewRequests().Resolve(t.Context(), db, "work", workID, &managev1.OgTargetSelection{
		Target: &managev1.OgTargetSelection_Locale{Locale: "ko"},
	})
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, "Stored Korean title", requests[0].Title)
	require.NotNil(t, requests[0].Locale)
	require.Equal(t, "ko", *requests[0].Locale)
}
