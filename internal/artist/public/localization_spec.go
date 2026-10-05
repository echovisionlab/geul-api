package public

import "github.com/echovisionlab/geul-api/internal/publiccontent"

var artistLocalizationSpec = publiccontent.Spec{
	EntityType:   "artist",
	TableName:    "artist_translation",
	SelectClause: "locale, title, NULL::text AS summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, og_asset_id",
}

var artistWorkLocalizationSpec = publiccontent.Spec{
	EntityType:   "work",
	TableName:    "work_translation",
	SelectClause: "locale, title, summary, NULL::jsonb AS content_json, content_html, content_text, og_asset_id",
}
