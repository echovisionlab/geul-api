package public

import (
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var workFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{workdomain.WorkSourceTitleSQL("work")}},
		"status": {Column: "work.status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: publicWorkStatusValues()},
		"type": {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
			managev1.WorkType_WORK_TYPE_MUSIC_PROJECT.String(), managev1.WorkType_WORK_TYPE_PORTFOLIO.String(),
			managev1.WorkType_WORK_TYPE_ARTICLE.String(), managev1.WorkType_WORK_TYPE_CONTRIBUTION.String(),
		}},
		"featured": {Column: "featured", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps},
		"map_place_id": {Column: "map_place_id", Type: queryutil.TypeID, AllowedOps: []commonv1.FilterOp{
			commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN,
			commonv1.FilterOp_FILTER_OP_IS_NULL, commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
		}, IsFK: true},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
		"period_year":  {Column: "year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"period_month": {Column: "month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"year":         {Column: "year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"month":        {Column: "month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"until_year":   {Column: "until_year", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"until_month":  {Column: "until_month", Type: queryutil.TypeNumber, AllowedOps: queryutil.NumberOps},
		"is_present":   {Column: "is_present", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps},
	},
}

var WorkSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title": workdomain.WorkSourceTitleSQL("work"), "published_at": "published_at", "updated_at": "updated_at",
	},
	DefaultSort: "published_at DESC",
}
