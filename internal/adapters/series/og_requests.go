package series

import "github.com/echovisionlab/geul-api/internal/og"

const sourceTitleSQL = "COALESCE((SELECT translation.title FROM series_translation AS translation " +
	"WHERE translation.entity_id = series.id AND translation.locale = series.source_locale LIMIT 1), '')"

func NewRequests() *og.LocalizedSource {
	return og.NewLocalizedSource(og.LocalizedRequestSpec{
		EntityType: "series", Table: "series", TranslationTable: "series_translation",
		SourceTitleExpression: sourceTitleSQL,
	})
}
