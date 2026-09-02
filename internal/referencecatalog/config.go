package referencecatalog

import queryutil "github.com/echovisionlab/geul-api/internal/query"

var (
	categoryFilterConfig = nameSearchFilterConfig()
	clientFilterConfig   = nameSearchFilterConfig()
	mapPlaceFilterConfig = &queryutil.FilterConfig{
		Fields: map[string]queryutil.FieldDef{
			"search": {
				Type:          queryutil.TypeText,
				AllowedOps:    queryutil.SearchOps,
				SearchColumns: []string{"name", "address"},
			},
		},
	}
	tagFilterConfig = nameSearchFilterConfig()
)

func nameSearchFilterConfig() *queryutil.FilterConfig {
	return &queryutil.FilterConfig{
		Fields: map[string]queryutil.FieldDef{
			"search": {
				Type:          queryutil.TypeText,
				AllowedOps:    queryutil.SearchOps,
				SearchColumns: []string{"name"},
			},
		},
	}
}
