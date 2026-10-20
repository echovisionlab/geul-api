package work

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestArtistSourceTitleSQLReadsTheSourceLocaleTitle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE artist (id TEXT PRIMARY KEY, source_locale TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE artist_translation (entity_id TEXT NOT NULL, locale TEXT NOT NULL, title TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO artist (id, source_locale) VALUES ('artist-1', 'ko'), ('artist-2', 'en')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO artist_translation (entity_id, locale, title) VALUES
		('artist-1', 'en', 'Echo'), ('artist-1', 'ko', '에코'), ('artist-2', 'ko', '번역만')`).Error)

	var names []string
	require.NoError(t, db.Table("artist").Select(ArtistSourceTitleSQL("artist")+" AS name").Order("artist.id").Pluck("name", &names).Error)
	// The source-locale title; an Artist without one reads as empty, never as a translation.
	require.Equal(t, []string{"에코", ""}, names)
}
