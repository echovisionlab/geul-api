package release

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var ReleaseAdminFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{ReleaseSourceTitleSQL("release")}},
	"type": {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		string(model.ReleaseTypeAlbum), string(model.ReleaseTypeSingle),
		string(model.ReleaseTypeEP), string(model.ReleaseTypeCompilation),
	}},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		string(model.ReleaseStatusDraft), string(model.ReleaseStatusPublished),
	}},
}}

func timestampProtoPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}
