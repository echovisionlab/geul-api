package og

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLocalizedRequestsUseExistingTargetValue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE work (
		id text PRIMARY KEY, title text, featured_image_file_id text, source_locale text NOT NULL
	);
	CREATE TABLE work_translation (
		entity_id text, locale text, title text
	)`).Error)
	entityID := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO work (id, title, source_locale) VALUES (?, 'Source title', 'en')", entityID).Error)
	require.NoError(t, db.Exec(`INSERT INTO work_translation (
		entity_id, locale, title
	) VALUES (?, 'ko', '현재 한국어 제목')`, entityID).Error)
	requests := NewLocalizedRequests(LocalizedRequestSpec{
		EntityType: "work", Table: "work", TranslationTable: "work_translation",
		SourceTitleExpression: "work.title", FeaturedImageExpression: "work.featured_image_file_id",
	})
	selection := &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Locale{Locale: "ko"}}

	resolved, err := requests.Resolve(t.Context(), db, entityID, selection)
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Equal(t, "현재 한국어 제목", resolved[0].Title)

	require.NoError(t, db.Exec("DELETE FROM work_translation WHERE entity_id = ? AND locale = 'ko'", entityID).Error)
	_, err = requests.Resolve(t.Context(), db, entityID, selection)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
