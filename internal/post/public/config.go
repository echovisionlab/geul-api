package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

var PostFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{postpostgres.PostSourceTitleSQL}},
		"status": {
			Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				string(model.PostStatusPublished),
				string(model.PostStatusArchived),
			},
		},
		"series_id": {Column: "series_id", Type: queryutil.TypeID, AllowedOps: []commonv1.FilterOp{
			commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN,
		}, IsFK: true},
		"map_place_id": {Column: "map_place_id", Type: queryutil.TypeID, AllowedOps: []commonv1.FilterOp{
			commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN,
			commonv1.FilterOp_FILTER_OP_IS_NULL, commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
		}, IsFK: true},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
	},
}

var PostSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title": postpostgres.PostSourceTitleSQL, "published_at": "published_at", "series_order": "series_order",
	},
	DefaultSort: "published_at DESC",
}
