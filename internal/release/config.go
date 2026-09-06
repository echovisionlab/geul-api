package release

import (
	"time"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var ReleaseAdminFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{ReleaseSourceTitleSQL("release")}},
	"type": {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		managev1.ReleaseType_RELEASE_TYPE_ALBUM.String(), managev1.ReleaseType_RELEASE_TYPE_SINGLE.String(),
		managev1.ReleaseType_RELEASE_TYPE_EP.String(), managev1.ReleaseType_RELEASE_TYPE_COMPILATION.String(),
	}},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(), managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(),
	}},
}}

func timestampProtoPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}
