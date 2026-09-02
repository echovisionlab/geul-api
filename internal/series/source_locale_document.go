// Package series owns Post Series source-locale documents and translation
// projections.
package series

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

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

// SourceLocaleDocument is the source-owned Series copy used for projection.
type SourceLocaleDocument struct {
	Title       *string
	Summary     *string
	ContentText *string
	OgAssetID   *string
}

type sourceLocaleDocumentRow struct {
	EntityID string         `gorm:"column:entity_id"`
	Title    sql.NullString `gorm:"column:title"`
	Summary  sql.NullString `gorm:"column:summary"`
	OgAsset  sql.NullString `gorm:"column:og_asset_id"`
}

// LoadSourceLocaleDocumentStates loads the source copy for each unique,
// non-empty Series ID. Missing IDs are omitted from the result.
func LoadSourceLocaleDocumentStates(
	ctx context.Context,
	db *gorm.DB,
	ids []string,
) (map[string]*SourceLocaleDocument, error) {
	ids = normalizeSourceLocaleDocumentIDs(ids)
	if len(ids) == 0 {
		return map[string]*SourceLocaleDocument{}, nil
	}

	var rows []sourceLocaleDocumentRow
	result := db.WithContext(ctx).Raw(
		`SELECT translation.entity_id::text AS entity_id, translation.title,
		        translation.summary, translation.og_asset_id
		 FROM series_translation AS translation
		 JOIN series AS root ON root.id = translation.entity_id
		  AND root.source_locale = translation.locale
		 WHERE translation.entity_id::text IN ?`,
		ids,
	).Scan(&rows)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}

	documents := make(map[string]*SourceLocaleDocument, len(rows))
	for _, row := range rows {
		document := &SourceLocaleDocument{}
		if row.Title.Valid {
			document.Title = stringPointer(row.Title.String)
		}
		if row.Summary.Valid {
			document.Summary = stringPointer(row.Summary.String)
			document.ContentText = stringPointer(row.Summary.String)
		}
		if row.OgAsset.Valid {
			document.OgAssetID = stringPointer(row.OgAsset.String)
		}
		documents[row.EntityID] = document
	}
	return documents, nil
}

// LoadRequiredSourceLocaleDocument returns the source copy for one Series.
func LoadRequiredSourceLocaleDocument(
	ctx context.Context,
	db *gorm.DB,
	seriesID string,
) (*SourceLocaleDocument, error) {
	documents, err := LoadSourceLocaleDocumentStates(ctx, db, []string{seriesID})
	if err != nil {
		return nil, err
	}
	document := documents[seriesID]
	if document == nil {
		return nil, errs.NotFound("series_translation", seriesID)
	}
	return document, nil
}

// OverlaySourceLocaleDocument projects source-localized fields onto a Series.
func OverlaySourceLocaleDocument(series *model.Series, document *SourceLocaleDocument) {
	if series == nil || document == nil {
		return
	}
	if document.Title != nil {
		series.Title = *document.Title
	}
	series.Description = document.Summary
	series.OgAssetID = document.OgAssetID
}

// LoadTranslationSourceDocument returns the source-owned Series fields the
// provider may receive.
func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	seriesID string,
) (*translation.SourceDocument, error) {
	document, err := LoadRequiredSourceLocaleDocument(ctx, db, seriesID)
	if err != nil {
		return nil, err
	}
	contentDocument, err := loadSeriesContentDocumentState(ctx, db, seriesID, false)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		SourceLocale:            contentDocument.SourceLocale,
		Title:                   derefString(document.Title),
		Summary:                 document.Summary,
		ContentText:             document.ContentText,
		ContentDocumentRevision: contentDocument.Revision.String(),
	}, nil
}

func normalizeSourceLocaleDocumentIDs(ids []string) []string {
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
