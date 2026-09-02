package public

import (
	"github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var (
	categoryFilterConfig = nameSearchFilterConfig()
	clientFilterConfig   = nameSearchFilterConfig()
	tagFilterConfig      = nameSearchFilterConfig()
)

func nameSearchFilterConfig() *query.FilterConfig {
	return &query.FilterConfig{
		Fields: map[string]query.FieldDef{
			"search": {
				Type:          query.TypeText,
				AllowedOps:    query.SearchOps,
				SearchColumns: []string{"name"},
			},
		},
	}
}

var tagSortConfig = &query.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"post_count": "post_count",
	},
	DefaultSort: "name ASC",
}

var categorySortConfig = &query.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"post_count": "post_count",
	},
	DefaultSort: "name ASC",
}

func postStatusValues() []string {
	return []string{
		managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
		managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
	}
}
