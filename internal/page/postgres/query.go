package pagepostgres

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

var pageFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps,
		SearchColumns: []string{pageSourceTitleSQL("page")},
	},
	"status": {
		Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps,
		EnumValues: []string{
			string(model.PageStatusDraft),
			string(model.PageStatusPublished),
		},
	},
}}

// pageSortConfig defines allowed sort fields for pages
var pageSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":        pageSourceTitleSQL("page"),
		"slug":         "slug",
		"status":       "status",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
		"show_title":   "show_title",
	},
	DefaultSort: "created_at DESC",
}

func pageSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "page"
	}
	return fmt.Sprintf(
		"(SELECT pt.%s FROM page_translation AS pt WHERE pt.entity_id = %s.id AND pt.locale = %s.source_locale LIMIT 1)",
		column,
		alias,
		alias,
	)
}

func pageSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(%s, '')", pageSourceLocaleColumnSQL(tableAlias, "title"))
}
