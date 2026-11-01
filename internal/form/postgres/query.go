package formpostgres

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/form"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

func formSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "form"
	}
	return fmt.Sprintf(
		"(SELECT ft.%s FROM form_translation AS ft "+
			"WHERE ft.entity_id = %s.id AND ft.locale = %s.source_locale LIMIT 1)",
		column,
		alias,
		alias,
	)
}

// FormSourceTitleSQL is the source title of the form row aliased
// tableAlias, the default title when it has none.
func FormSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf(
		"COALESCE(NULLIF(BTRIM(%s), ''), '%s')",
		formSourceLocaleColumnSQL(tableAlias, "title"),
		form.DefaultTitle,
	)
}

// TranslationEntrySelectSQL returns the Form-owned management projection.
func TranslationEntrySelectSQL() string {
	return `SELECT locale, title, NULL::text AS summary,
		NULL::text AS content_html, content_text, content_json,
		updated_at, og_asset_id FROM form_translation`
}

// FormFilterConfig is the admin list's filters.
var FormFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{FormSourceTitleSQL("form")},
		},
		"status": {
			Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps,
			EnumValues: []string{string(form.StatusDraft), string(form.StatusPublished)},
		},
	},
}

// formSortConfig defines allowed sort fields for forms
var formSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at": "created_at",
		"updated_at": "updated_at",
		"title":      FormSourceTitleSQL("form"),
		"status":     "status",
		"slug":       "slug",
	},
	DefaultSort: "created_at DESC",
}
