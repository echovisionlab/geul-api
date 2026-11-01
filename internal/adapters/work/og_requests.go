package work

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

const sourceTitleSQL = `COALESCE((SELECT translation.title FROM work_translation AS translation
	JOIN work AS source ON source.id = translation.entity_id
		AND source.source_locale = translation.locale
	WHERE translation.entity_id = work.id LIMIT 1), '')`

// NewRequests reads exact-current Work locale snapshots for OG generation.
func NewRequests() *ogpostgres.LocalizedSource {
	return ogpostgres.NewLocalizedSource(ogpostgres.LocalizedRequestSpec{
		EntityType: "work", Table: "work", TranslationTable: "work_translation",
		SourceTitleExpression: sourceTitleSQL,
	})
}
