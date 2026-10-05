package referencecatalog

import queryutil "github.com/echovisionlab/geul-api/internal/query"

var (
	categoryFilterConfig = nameSearchFilterConfig()
	clientFilterConfig   = nameSearchFilterConfig()
	formatFilterConfig   = nameSearchFilterConfig()
	genreFilterConfig    = nameSearchFilterConfig()
	mapPlaceFilterConfig = &queryutil.FilterConfig{
		Fields: map[string]queryutil.FieldDef{
			"search": {
				Type:          queryutil.TypeText,
				AllowedOps:    queryutil.SearchOps,
				SearchColumns: []string{"name", "address"},
			},
		},
	}
	styleFilterConfig = nameSearchFilterConfig()
	tagFilterConfig   = nameSearchFilterConfig()
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
