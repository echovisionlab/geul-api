package page

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

const sourceTitleSQL = "COALESCE((SELECT translation.title FROM page_translation AS translation " +
	"WHERE translation.entity_id = page.id AND translation.locale = page.source_locale LIMIT 1), '')"

func NewRequests() *ogpostgres.LocalizedSource {
	return ogpostgres.NewLocalizedSource(ogpostgres.LocalizedRequestSpec{
		EntityType: "page", Table: "page", TranslationTable: "page_translation",
		SourceTitleExpression: sourceTitleSQL,
	})
}
