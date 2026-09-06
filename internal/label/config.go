package label

import (
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var LabelFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search":          {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{LabelSourceTitleSQL("label")}},
	"country_code":    {Column: "country_code", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps},
	"parent_label_id": {Column: "parent_label_id", Type: queryutil.TypeID, AllowedOps: queryutil.IDOps, IsFK: true},
	"status":          {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{managev1.LabelStatus_LABEL_STATUS_DRAFT.String(), managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String()}},
}}
