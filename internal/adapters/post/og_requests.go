package post

import "github.com/echovisionlab/geul-api/internal/og"

const sourceTitleSQL = "COALESCE((SELECT translation.title FROM post_translation AS translation " +
	"WHERE translation.entity_id = post.id AND translation.locale = post.source_locale LIMIT 1), '')"

func NewRequests() *og.LocalizedSource {
	return og.NewLocalizedSource(og.LocalizedRequestSpec{
		EntityType: "post", Table: "post", TranslationTable: "post_translation",
		SourceTitleExpression: sourceTitleSQL,
	})
}
