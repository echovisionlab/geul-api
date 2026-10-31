package page

import "github.com/echovisionlab/geul-api/internal/og"

const sourceTitleSQL = "COALESCE((SELECT translation.title FROM page_translation AS translation " +
	"WHERE translation.entity_id = page.id AND translation.locale = page.source_locale LIMIT 1), '')"

func NewRequests() *og.LocalizedSource {
	return og.NewLocalizedSource(og.LocalizedRequestSpec{
		EntityType: "page", Table: "page", TranslationTable: "page_translation",
		SourceTitleExpression: sourceTitleSQL,
	})
}
