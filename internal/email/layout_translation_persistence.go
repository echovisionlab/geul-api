package email

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

// LayoutTranslationDocument is the locale-owned raw wrapper state for one
// Email Layout.
type LayoutTranslationDocument struct {
	ContentHTML *string
	ContentText *string
	UpdatedAt   time.Time
}

// LayoutTranslationEntry is the persisted Email Layout translation row used
// by common Translation lifecycle orchestration.
type LayoutTranslationEntry struct {
	LayoutTranslationDocument
}

// CanonicalLayoutProjection is the source wrapper used by Email Layout list
// and detail projections.
type CanonicalLayoutProjection struct {
	LayoutID    string
	ContentHTML string
}

// LayoutLocaleSnapshot is one immutable delivery-snapshot input row.
type LayoutLocaleSnapshot struct {
	Locale         string
	ContentHTML    *string
	IsSourceLocale bool
}

// LayoutTranslationEntrySelectSQL returns the canonical management projection
// for the Email Layout translation table.
func LayoutTranslationEntrySelectSQL() string {
	return `SELECT locale, NULL::text AS title, NULL::text AS summary,
		html_content AS content_html, content_text, NULL::jsonb AS content_json,
		updated_at,
		NULL::uuid AS og_asset_id FROM email_layout_translation`
}

// LoadCanonicalLayoutProjections loads the exact source row for every requested
// layout. Missing source rows are omitted.
func LoadCanonicalLayoutProjections(
	ctx context.Context,
	db *gorm.DB,
	layoutIDs []string,
) (map[string]CanonicalLayoutProjection, error) {
	if len(layoutIDs) == 0 {
		return map[string]CanonicalLayoutProjection{}, nil
	}
	var rows []struct {
		LayoutID string  `gorm:"column:entity_id"`
		HTML     *string `gorm:"column:html_content"`
	}
	if err := db.WithContext(ctx).
		Table("email_layout_translation AS layout").
		Select("layout.entity_id, layout.html_content").
		Joins("JOIN email_layout AS root ON root.id = layout.entity_id AND root.source_locale = layout.locale").
		Where("layout.entity_id IN ?", layoutIDs).
		Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	projections := make(map[string]CanonicalLayoutProjection, len(rows))
	for _, row := range rows {
		if row.HTML == nil {
			continue
		}
		canonical, _, err := MaterializeLayoutSourceLocale(*row.HTML)
		if err != nil {
			return nil, errs.Internal(err)
		}
		projections[row.LayoutID] = CanonicalLayoutProjection{
			LayoutID: row.LayoutID, ContentHTML: NormalizeTemplatePlaceholders(dereferenceString(canonical)),
		}
	}
	return projections, nil
}

// LoadLayoutLocaleSnapshots returns every existing locale row in the exact
// deterministic order used to seal delivery snapshots.
func LoadLayoutLocaleSnapshots(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
) ([]LayoutLocaleSnapshot, error) {
	var rows []struct {
		Locale         string  `gorm:"column:locale"`
		ContentHTML    *string `gorm:"column:html_content"`
		IsSourceLocale bool    `gorm:"column:is_source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("email_layout_translation AS entry").
		Select("entry.locale, entry.html_content, entry.locale = root.source_locale AS is_source_locale").
		Joins("JOIN email_layout AS root ON root.id = entry.entity_id").
		Where("entry.entity_id = ?", layoutID).
		Order("is_source_locale DESC, locale ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	var storedSourceHTML *string
	for _, row := range rows {
		if row.IsSourceLocale {
			storedSourceHTML = row.ContentHTML
			break
		}
	}
	if storedSourceHTML == nil {
		return nil, errs.NotFound("email layout source translation", layoutID)
	}
	sourceHTML, _, err := MaterializeLayoutSourceLocale(*storedSourceHTML)
	if err != nil {
		return nil, errs.Internal(err)
	}
	snapshots := make([]LayoutLocaleSnapshot, 0, len(rows))
	for _, row := range rows {
		resolved, err := ResolveLayoutLocaleMarkup(dereferenceString(sourceHTML), func() *string {
			if row.IsSourceLocale {
				return nil
			}
			return row.ContentHTML
		}())
		if err != nil {
			return nil, errs.Internal(err)
		}
		snapshots = append(snapshots, LayoutLocaleSnapshot{
			Locale: row.Locale, ContentHTML: stringPointer(resolved),
			IsSourceLocale: row.IsSourceLocale,
		})
	}
	return snapshots, nil
}

// LoadLayoutTranslationEntry loads one locale row, returning nil when absent.
func LoadLayoutTranslationEntry(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	locale string,
) (*LayoutTranslationEntry, error) {
	var row struct {
		ContentHTML sql.NullString `gorm:"column:html_content"`
		ContentText sql.NullString `gorm:"column:content_text"`
		UpdatedAt   time.Time      `gorm:"column:updated_at"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT html_content, content_text, updated_at
		 FROM email_layout_translation
		 WHERE entity_id = ? AND locale = ?
		 LIMIT 1`,
		layoutID,
		locale,
	).Scan(&row)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}

	entry := &LayoutTranslationEntry{
		LayoutTranslationDocument: LayoutTranslationDocument{
			UpdatedAt: row.UpdatedAt,
		},
	}
	if row.ContentHTML.Valid {
		normalized := NormalizeTemplatePlaceholders(row.ContentHTML.String)
		entry.ContentHTML = &normalized
	}
	if row.ContentText.Valid {
		entry.ContentText = stringPointer(row.ContentText.String)
	}
	return entry, nil
}

// LoadLayoutTranslationDocument returns one required locale document.
func LoadLayoutTranslationDocument(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	locale string,
) (*LayoutTranslationDocument, error) {
	entry, err := LoadLayoutTranslationEntry(ctx, db, layoutID, locale)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, errs.NotFound("email layout translation", layoutID+":"+locale)
	}
	document := entry.LayoutTranslationDocument
	if document.ContentText == nil && document.ContentHTML != nil {
		document.ContentText = stringPointer(StripHTML(*document.ContentHTML))
	}
	return &document, nil
}

// ResolveLayoutTranslationSourceLocale resolves the exact authoring source
// authority. The caller owns fallback policy when no persisted authority exists.
func ResolveLayoutTranslationSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
) (string, bool, error) {
	var sourceRow struct {
		Locale string `gorm:"column:locale"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT source_locale AS locale
		 FROM email_layout
		 WHERE id = ?
		 LIMIT 1`,
		layoutID,
	).Scan(&sourceRow)
	if result.Error != nil {
		return "", false, errs.Internal(result.Error)
	}
	if result.RowsAffected > 0 && strings.TrimSpace(sourceRow.Locale) != "" {
		return sourceRow.Locale, true, nil
	}
	return "", false, nil
}

// LoadCanonicalLayoutTranslationDocument loads the required source-locale row.
func LoadCanonicalLayoutTranslationDocument(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	sourceLocale string,
) (*LayoutTranslationDocument, error) {
	sourceLocale = strings.TrimSpace(sourceLocale)
	if sourceLocale == "" {
		return nil, errs.NotFound("email layout source translation", layoutID)
	}
	document, err := LoadLayoutTranslationDocument(ctx, db, layoutID, sourceLocale)
	if err != nil {
		return nil, err
	}
	if document.ContentHTML == nil {
		return nil, errs.FailedPrecondition("Email Layout source locale content is missing")
	}
	contentHTML, contentText, err := MaterializeLayoutSourceLocale(*document.ContentHTML)
	if err != nil {
		return nil, errs.FailedPrecondition("Email Layout source locale markers are invalid")
	}
	document.ContentHTML = contentHTML
	document.ContentText = contentText
	return document, nil
}

// LoadLayoutTranslationSourceDocument returns the provider-visible source.
func LoadLayoutTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	sourceLocale string,
) (*translation.SourceDocument, error) {
	document, err := LoadCanonicalLayoutTranslationDocument(ctx, db, layoutID, sourceLocale)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		ContentHTML: document.ContentHTML,
		ContentText: document.ContentText,
	}, nil
}

func loadLayoutSourceLocaleFlag(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
) (string, bool, error) {
	var row struct {
		Locale string `gorm:"column:locale"`
	}
	err := db.WithContext(ctx).
		Table("email_layout").
		Select("source_locale AS locale").
		Where("id = ?", layoutID).
		Take(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", false, nil
		}
		return "", false, errs.Internal(err)
	}
	locale := strings.TrimSpace(row.Locale)
	return locale, locale != "", nil
}

func stringPointer(value string) *string {
	return &value
}
