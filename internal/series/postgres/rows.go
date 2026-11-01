package postgres

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/series"
)

const contentDocumentProfile = "compact"

// SourceTitleSQL returns the source-locale title projection for a Series
// table alias. It is intended for queries that need the authoritative title.
func SourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "series"
	}
	return fmt.Sprintf(
		"COALESCE((SELECT st.title FROM series_translation AS st WHERE st.entity_id = %s.id "+
			"AND st.locale = %s.source_locale LIMIT 1), '')",
		alias,
		alias,
	)
}

var seriesFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{SourceTitleSQL("series")}},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		string(model.SeriesStatusDraft),
		string(model.SeriesStatusPublished),
	}},
}}

var seriesSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at": "created_at",
		"updated_at": "updated_at",
		"title":      SourceTitleSQL("series"),
		"status":     "status",
	},
	DefaultSort: "created_at DESC",
}

func seriesFromRow(row model.Series) series.Series {
	return series.Series{
		ID:                  row.ID,
		ContentDocumentID:   row.ContentDocumentID,
		Slug:                row.Slug,
		Status:              series.Status(row.Status),
		SourceLocale:        row.SourceLocale,
		FeaturedImageFileID: row.FeaturedImageFileID,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

func seriesFromRows(rows []model.Series) []series.Series {
	result := make([]series.Series, len(rows))
	for i := range rows {
		result[i] = seriesFromRow(rows[i])
	}
	return result
}

func seriesRow(value series.Series) model.Series {
	return model.Series{
		ID:                  value.ID,
		ContentDocumentID:   value.ContentDocumentID,
		Slug:                value.Slug,
		Status:              model.SeriesStatus(value.Status),
		SourceLocale:        value.SourceLocale,
		FeaturedImageFileID: value.FeaturedImageFileID,
		CreatedAt:           value.CreatedAt,
		UpdatedAt:           value.UpdatedAt,
	}
}

func translationFromRow(row model.SeriesTranslation) series.Translation {
	return series.Translation{
		SeriesID:    row.EntityID,
		Locale:      row.Locale,
		Title:       row.Title,
		Summary:     row.Summary,
		ContentJSON: row.ContentJSON,
		ContentHTML: row.ContentHTML,
		ContentText: row.ContentText,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		OgAssetID:   row.OgAssetID,
	}
}

func translationRow(value series.Translation) model.SeriesTranslation {
	return model.SeriesTranslation{
		EntityID:    value.SeriesID,
		Locale:      value.Locale,
		Title:       value.Title,
		Summary:     value.Summary,
		ContentJSON: value.ContentJSON,
		ContentHTML: value.ContentHTML,
		ContentText: value.ContentText,
		CreatedAt:   value.CreatedAt,
		UpdatedAt:   value.UpdatedAt,
		OgAssetID:   value.OgAssetID,
	}
}

func nullString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func stringPointer(value string) *string {
	return &value
}

func jsonValueOrNil(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func sortedStrings(values []string) []string {
	slices.Sort(values)
	return values
}
