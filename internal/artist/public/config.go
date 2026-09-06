package public

import (
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var ArtistFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search":       {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{artistdomain.ArtistSourceTitleSQL("artist"), "real_name"}},
		"status":       {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String(), managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()}},
		"country_code": {Column: "country_code", Type: queryutil.TypeText, AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN}},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
	},
	DefaultFilters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ, Value: managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String()}},
}

var ArtistSortConfig = &queryutil.SortConfig{AllowedFields: map[string]string{"name": artistdomain.ArtistSourceTitleSQL("artist"), "published_at": "published_at"}, DefaultSort: artistdomain.ArtistSourceTitleSQL("artist") + " ASC"}
