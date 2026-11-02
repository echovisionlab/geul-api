package postgres

import (
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

var LabelFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"id":           {Column: "id", Type: queryutil.TypeID, AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_IN}},
		"search":       {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{labelSourceTitleSQL("label")}},
		"status":       {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{string(model.LabelStatusDraft), string(model.LabelStatusPublished)}},
		"country_code": {Column: "country_code", Type: queryutil.TypeText, AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN}},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
	},
}

var LabelSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{"name": labelSourceTitleSQL("label"), "published_at": "published_at"},
	DefaultSort:   labelSourceTitleSQL("label") + " ASC",
}
