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

// UpsertLayoutTranslationEntry persists one Email Layout locale entry.
func UpsertLayoutTranslationEntry(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	locale string,
	input translation.EntryWrite,
) error {
	if input.ContentHTML != nil {
		normalized := NormalizeTemplatePlaceholders(*input.ContentHTML)
		if err := ValidateLayoutHTMLContentError(normalized); err != nil {
			return errs.InvalidArgument("html_content", err.Error())
		}
		input.ContentHTML = &normalized
	}

	return db.WithContext(ctx).Exec(
		`INSERT INTO email_layout_translation (
			entity_id, locale, html_content, content_text,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			html_content = EXCLUDED.html_content,
			content_text = EXCLUDED.content_text,
			updated_at = EXCLUDED.updated_at`,
		layoutID,
		locale,
		input.ContentHTML,
		input.ContentText,
		input.Now,
		input.Now,
	).Error
}

// SaveLayoutSourceLocaleDocument persists the canonical source wrapper and
// makes it the only source-locale row.
func SaveLayoutSourceLocaleDocument(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	locale string,
	document LayoutTranslationDocument,
	now time.Time,
) error {
	htmlContent := NormalizeTemplatePlaceholders(dereferenceString(document.ContentHTML))
	canonical, err := CanonicalizeLayoutSourceMarkers(htmlContent)
	if err != nil {
		return errs.InvalidArgument("html_content", err.Error())
	}
	htmlContent = canonical
	contentText := document.ContentText
	if contentText == nil || strings.TrimSpace(dereferenceString(contentText)) == "" {
		contentText = stringPointer(StripHTML(htmlContent))
	}
	if err := UpsertLayoutTranslationEntry(ctx, db, layoutID, locale, translation.EntryWrite{
		ContentHTML: &htmlContent,
		ContentText: contentText,
		Now:         now,
	}); err != nil {
		return err
	}
	return normalizeLayoutTargetRepresentations(ctx, db, layoutID, locale, htmlContent)
}

// PrepareLayoutSourceLocaleSwitch makes only the requested locale safe to
// assume the source role. Existing locale values and updated_at are preserved;
// an absent row receives one complete explicit-empty value wrapper.
func PrepareLayoutSourceLocaleSwitch(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	currentSourceLocale string,
	requestedLocale string,
	now time.Time,
) error {
	current, err := LoadCanonicalLayoutTranslationDocument(
		ctx,
		db,
		layoutID,
		currentSourceLocale,
	)
	if err != nil {
		return err
	}
	if current.ContentHTML == nil {
		return errs.FailedPrecondition("Email Layout source locale content is missing")
	}
	entry, err := LoadLayoutTranslationEntry(ctx, db, layoutID, requestedLocale)
	if err != nil {
		return err
	}
	var contentHTML, contentText *string
	if entry == nil || entry.ContentHTML == nil {
		contentHTML, contentText, err = EmptyLayoutLocaleFromSource(*current.ContentHTML)
	} else {
		contentHTML, contentText, err = NormalizeLayoutLocaleRepresentation(
			*current.ContentHTML,
			*entry.ContentHTML,
		)
	}
	if err != nil {
		return errs.FailedPrecondition("Email Layout locale markers require repair before source-locale switching")
	}
	if entry == nil {
		return UpsertLayoutTranslationEntry(ctx, db, layoutID, requestedLocale, translation.EntryWrite{
			ContentHTML: contentHTML,
			ContentText: contentText,
			Now:         now.UTC(),
		})
	}
	if dereferenceString(entry.ContentHTML) == dereferenceString(contentHTML) &&
		dereferenceString(entry.ContentText) == dereferenceString(contentText) {
		return nil
	}
	return db.WithContext(ctx).Exec(
		`UPDATE email_layout_translation
		 SET html_content = ?, content_text = ?
		 WHERE entity_id = ? AND locale = ?`,
		contentHTML,
		contentText,
		layoutID,
		requestedLocale,
	).Error
}

func normalizeLayoutTargetRepresentations(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	sourceLocale string,
	canonicalSource string,
) error {
	var rows []struct {
		Locale      string         `gorm:"column:locale"`
		ContentHTML sql.NullString `gorm:"column:html_content"`
		ContentText sql.NullString `gorm:"column:content_text"`
	}
	if err := db.WithContext(ctx).Table("email_layout_translation").
		Select("locale, html_content, content_text").
		Where("entity_id = ? AND locale <> ?", layoutID, sourceLocale).
		Order("locale ASC").Scan(&rows).Error; err != nil {
		return errs.Internal(err)
	}
	for _, row := range rows {
		if !row.ContentHTML.Valid {
			continue
		}
		nextHTML, nextText, err := NormalizeLayoutLocaleRepresentation(
			canonicalSource,
			row.ContentHTML.String,
		)
		if err != nil {
			return errs.FailedPrecondition("Email Layout target markers require backfill before source editing")
		}
		if dereferenceString(nextHTML) == row.ContentHTML.String &&
			row.ContentText.Valid && dereferenceString(nextText) == row.ContentText.String {
			continue
		}
		if err := db.WithContext(ctx).Exec(
			`UPDATE email_layout_translation
			 SET html_content = ?, content_text = ?
			 WHERE entity_id = ? AND locale = ?`,
			nextHTML, nextText, layoutID, row.Locale,
		).Error; err != nil {
			return errs.Internal(err)
		}
	}
	return nil
}

// SeedLayoutTranslationEntryFromSource creates one target row whose locale
// values are an explicit copy of the current source values. Public fallback
// never calls this function; explicit target creation does.
func SeedLayoutTranslationEntryFromSource(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	locale string,
	sourceLocale string,
	now time.Time,
) error {
	if strings.TrimSpace(sourceLocale) == "" {
		return errs.InvalidArgument("source_locale", "source locale is required")
	}
	if locale == sourceLocale {
		return errs.FailedPrecondition("Email Layout source locale changed while seeding target metadata")
	}
	source, err := LoadCanonicalLayoutTranslationDocument(ctx, db, layoutID, sourceLocale)
	if err != nil {
		return err
	}
	sourceHTML := dereferenceString(source.ContentHTML)
	units, err := ExtractLayoutContentUnits(sourceHTML)
	if err != nil {
		return errs.FailedPrecondition("Email Layout source unit markers require repair before creating a target")
	}
	values := make(map[string]string, len(units))
	for _, unit := range units {
		values[unit.Handle] = unit.SourceValue
	}
	targetHTML, targetText, err := ApplyLayoutLocaleValues(sourceHTML, values)
	if err != nil {
		return errs.Internal(err)
	}
	return db.WithContext(ctx).Exec(`
		INSERT INTO email_layout_translation (
			entity_id, locale, html_content, content_text, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO NOTHING`,
		layoutID, locale, targetHTML, targetText, now, now,
	).Error
}
