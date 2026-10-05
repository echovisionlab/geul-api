package public

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/publiccontent"
)

var labelLocalizationSpec = publiccontent.Spec{
	EntityType:   "label",
	TableName:    "label_translation",
	SelectClause: "locale, title, NULL::text AS summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, NULL::uuid AS og_asset_id",
}

func sourceColumnSQL(alias, entity, table, translationAlias, column string) string {
	return fmt.Sprintf("(SELECT %s.%s FROM %s AS %s JOIN %s AS source ON source.id = %s.entity_id AND source.source_locale = %s.locale WHERE %s.entity_id = %s.id LIMIT 1)", translationAlias, column, table, translationAlias, entity, translationAlias, translationAlias, translationAlias, alias)
}

func labelSourceTitleSQL(alias string) string {
	return "COALESCE(NULLIF(" + sourceColumnSQL(alias, "label", "label_translation", "lt", "title") + ", ''), '')"
}

func artistSourceTitleSQL(alias string) string {
	return "COALESCE(NULLIF(" + sourceColumnSQL(alias, "artist", "artist_translation", "at", "title") + ", ''), '')"
}

func releaseSourceTitleSQL(alias string) string {
	return "COALESCE(NULLIF(" + sourceColumnSQL(alias, "release", "release_translation", "rt", "title") + ", ''), '')"
}
