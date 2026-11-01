package series

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

const sourceTitleSQL = "COALESCE((SELECT translation.title FROM series_translation AS translation " +
	"WHERE translation.entity_id = series.id AND translation.locale = series.source_locale LIMIT 1), '')"

func NewRequests() *ogpostgres.LocalizedSource {
	return ogpostgres.NewLocalizedSource(ogpostgres.LocalizedRequestSpec{
		EntityType: "series", Table: "series", TranslationTable: "series_translation",
		SourceTitleExpression: sourceTitleSQL,
	})
}
