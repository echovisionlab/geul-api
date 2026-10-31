package series

import (
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

var SeriesFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{SourceTitleSQL("series")}},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		string(model.SeriesStatusDraft),
		string(model.SeriesStatusPublished),
	}},
}}
