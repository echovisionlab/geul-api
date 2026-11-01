package post

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

const sourceTitleSQL = "COALESCE((SELECT translation.title FROM post_translation AS translation " +
	"WHERE translation.entity_id = post.id AND translation.locale = post.source_locale LIMIT 1), '')"

func NewRequests() *ogpostgres.LocalizedSource {
	return ogpostgres.NewLocalizedSource(ogpostgres.LocalizedRequestSpec{
		EntityType: "post", Table: "post", TranslationTable: "post_translation",
		SourceTitleExpression: sourceTitleSQL,
	})
}
