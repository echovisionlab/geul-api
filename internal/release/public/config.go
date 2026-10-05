package public

import (
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var ReleaseFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{releasepkg.ReleaseSourceTitleSQL("release")}},
		"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
			managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(),
		}},
		"type": {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
			managev1.ReleaseType_RELEASE_TYPE_ALBUM.String(), managev1.ReleaseType_RELEASE_TYPE_EP.String(),
			managev1.ReleaseType_RELEASE_TYPE_SINGLE.String(), managev1.ReleaseType_RELEASE_TYPE_COMPILATION.String(),
		}},
		"release_date": {Column: "release_date", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
	},
}

var ReleaseSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{"title": releasepkg.ReleaseSourceTitleSQL("release"), "release_date": "release_date", "published_at": "published_at"},
	DefaultSort:   "release_date DESC NULLS LAST",
}
