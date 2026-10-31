package label

import (
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

var LabelFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search":          {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{LabelSourceTitleSQL("label")}},
	"country_code":    {Column: "country_code", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps},
	"parent_label_id": {Column: "parent_label_id", Type: queryutil.TypeID, AllowedOps: queryutil.IDOps, IsFK: true},
	"status":          {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{string(model.LabelStatusDraft), string(model.LabelStatusPublished)}},
}}
