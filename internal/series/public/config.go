package public

import (
	"github.com/echovisionlab/geul-api/internal/query"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var SeriesFilterConfig = &query.FilterConfig{
	Fields: map[string]query.FieldDef{
		"search": {Type: query.TypeText, AllowedOps: query.SearchOps, SearchColumns: []string{seriesdomain.SourceTitleSQL("s")}},
		"status": {Column: "status", Type: query.TypeEnum, AllowedOps: query.EnumOps, EnumValues: []string{
			managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
			managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(),
		}},
	},
	DefaultFilters: []*commonv1.FilterSpec{{
		Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ,
		Value: managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
	}},
}

var SeriesSortConfig = &query.SortConfig{
	AllowedFields: map[string]string{
		"title": seriesdomain.SourceTitleSQL("s"), "name": seriesdomain.SourceTitleSQL("s"),
		"post_count": "post_count",
	},
	DefaultSort: seriesdomain.SourceTitleSQL("s") + " ASC",
}
