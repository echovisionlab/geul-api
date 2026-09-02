package public

import "github.com/echovisionlab/geul-api/internal/publiccontent"

var workLocalizationSpec = publiccontent.Spec{
	EntityType:   "work",
	TableName:    "work_translation",
	SelectClause: "locale, title, summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, og_asset_id",
}
