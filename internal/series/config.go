package series

import (
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var SeriesFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{SourceTitleSQL("series")}},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
		managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(),
	}},
}}
