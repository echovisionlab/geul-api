package artist

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

const sourceTitleSQL = `COALESCE((SELECT translation.title FROM artist_translation AS translation
	JOIN artist AS source ON source.id = translation.entity_id
		AND source.source_locale = translation.locale
	WHERE translation.entity_id = artist.id LIMIT 1), '')`

const featuredImageSQL = `(SELECT artist_file.file_id FROM artist_file
	WHERE artist_file.artist_id = artist.id ORDER BY artist_file.sort_order ASC LIMIT 1)`

// NewRequests reads exact-current Artist locale snapshots for OG generation.
func NewRequests() *ogpostgres.LocalizedSource {
	return ogpostgres.NewLocalizedSource(ogpostgres.LocalizedRequestSpec{
		EntityType: "artist", Table: "artist", TranslationTable: "artist_translation",
		SourceTitleExpression:   sourceTitleSQL,
		FeaturedImageExpression: featuredImageSQL,
	})
}
