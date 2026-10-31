package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/query"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

var SeriesFilterConfig = &query.FilterConfig{
	Fields: map[string]query.FieldDef{
		"search": {Type: query.TypeText, AllowedOps: query.SearchOps, SearchColumns: []string{seriesdomain.SourceTitleSQL("s")}},
		"status": {Column: "status", Type: query.TypeEnum, AllowedOps: query.EnumOps, EnumValues: []string{
			string(model.SeriesStatusDraft),
			string(model.SeriesStatusPublished),
		}},
	},
	DefaultFilters: []*commonv1.FilterSpec{{
		Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ,
		Value: string(model.SeriesStatusDraft),
	}},
}

var SeriesSortConfig = &query.SortConfig{
	AllowedFields: map[string]string{
		"title": seriesdomain.SourceTitleSQL("s"), "name": seriesdomain.SourceTitleSQL("s"),
		"post_count": "post_count",
	},
	DefaultSort: seriesdomain.SourceTitleSQL("s") + " ASC",
}
