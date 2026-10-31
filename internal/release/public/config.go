package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
)

var ReleaseFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{releasepkg.ReleaseSourceTitleSQL("release")}},
		"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
			string(model.ReleaseStatusPublished),
		}},
		"type": {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
			string(model.ReleaseTypeAlbum), string(model.ReleaseTypeEP),
			string(model.ReleaseTypeSingle), string(model.ReleaseTypeCompilation),
		}},
		"release_date": {Column: "release_date", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
	},
}

var ReleaseSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{"title": releasepkg.ReleaseSourceTitleSQL("release"), "release_date": "release_date", "published_at": "published_at"},
	DefaultSort:   "release_date DESC NULLS LAST",
}
