package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/query"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// seriesFilterConfig is the public Series filter language over the s alias.
var seriesFilterConfig = &query.FilterConfig{
	Fields: map[string]query.FieldDef{
		"search": {Type: query.TypeText, AllowedOps: query.SearchOps, SearchColumns: []string{seriespostgres.SourceTitleSQL("s")}},
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

var seriesSortConfig = &query.SortConfig{
	AllowedFields: map[string]string{
		"title": seriespostgres.SourceTitleSQL("s"), "name": seriespostgres.SourceTitleSQL("s"),
		"post_count": "post_count",
	},
	DefaultSort: seriespostgres.SourceTitleSQL("s") + " ASC",
}
