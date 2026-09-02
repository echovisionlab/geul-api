package emailauthoring

import queryutil "github.com/echovisionlab/geul-api/internal/query"

// EmailTemplateFilterConfig defines filter fields for ListEmailTemplatesAdmin.
var EmailTemplateFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name", "key"},
		},
	},
}
