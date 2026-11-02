package emailauthoringpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
)

// LayoutTranslationEntrySelectSQL returns the canonical management projection
// for the Email Layout translation table.
func LayoutTranslationEntrySelectSQL() string {
	return `SELECT locale, NULL::text AS title, NULL::text AS summary,
		html_content AS content_html, content_text, NULL::jsonb AS content_json,
		updated_at,
		NULL::uuid AS og_asset_id FROM email_layout_translation`
}

// NewRenderingStore binds template and layout rendering to db.
func NewRenderingStore(db *gorm.DB) email.AuthoringStore {
	return renderingStore{db: db}
}

type renderingStore struct{ db *gorm.DB }

var _ email.AuthoringStore = renderingStore{}

// templateNotFoundError keeps the driver's not-found error readable and in
// the chain while it answers email.ErrTemplateNotFound.
type templateNotFoundError struct{ error }

func (err templateNotFoundError) Is(target error) bool { return target == email.ErrTemplateNotFound }
func (err templateNotFoundError) Unwrap() error        { return err.error }

func templateLookup(template model.EmailTemplate, err error) (email.Template, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return TemplateFromRow(template), templateNotFoundError{err}
	}
	return TemplateFromRow(template), err
}

func (s renderingStore) ActiveTemplateByEventKey(ctx context.Context, eventKey string) (email.Template, error) {
	var template model.EmailTemplate
	err := s.db.WithContext(ctx).
		Where("event_key = ?", eventKey).
		Where("is_active = ?", true).
		First(&template).Error
	return templateLookup(template, err)
}

func (s renderingStore) TemplateByID(ctx context.Context, templateID string) (email.Template, error) {
	var template model.EmailTemplate
	err := s.db.WithContext(ctx).First(&template, "id = ?", templateID).Error
	return templateLookup(template, err)
}

// sourceLocale reads the source_locale column of the table's row id.
func (s renderingStore) sourceLocale(ctx context.Context, table, id string) (string, bool, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	err := s.db.WithContext(ctx).
		Table(table).
		Select("source_locale").
		Where("id = ?", id).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.SourceLocale, true, nil
}

func (s renderingStore) TemplateSourceLocale(ctx context.Context, templateID string) (string, bool, error) {
	return s.sourceLocale(ctx, "email_template", templateID)
}

type templateContentRow struct {
	Locale      string  `gorm:"column:locale"`
	Subject     *string `gorm:"column:subject"`
	ContentHTML *string `gorm:"column:content_html"`
}

func (row templateContentRow) content() email.TemplateContent {
	return email.TemplateContent{Locale: row.Locale, Subject: row.Subject, ContentHTML: row.ContentHTML}
}

func (s renderingStore) TemplateContent(ctx context.Context, templateID, locale string) (*email.TemplateContent, error) {
	var row templateContentRow
	if err := s.db.WithContext(ctx).
		Table("email_template_translation").
		Select("locale, subject, content_html").
		Where("entity_id = ? AND locale = ?", templateID, locale).
		Where("content_html IS NOT NULL").
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	content := row.content()
	return &content, nil
}

func (s renderingStore) TemplateTranslations(ctx context.Context, templateID string, locales []string) ([]email.TemplateContent, error) {
	var rows []templateContentRow
	if err := s.db.WithContext(ctx).Table("email_template_translation").
		Select("locale, subject, content_html").
		Where("entity_id = ? AND locale IN ?", templateID, locales).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	contents := make([]email.TemplateContent, 0, len(rows))
	for _, row := range rows {
		contents = append(contents, row.content())
	}
	return contents, nil
}

func (s renderingStore) LayoutSourceLocale(ctx context.Context, layoutID string) (string, bool, error) {
	return s.sourceLocale(ctx, "email_layout", layoutID)
}

func (s renderingStore) LayoutContent(ctx context.Context, layoutID, locale string) (*string, error) {
	var row struct {
		HTMLContent *string `gorm:"column:html_content"`
	}
	if err := s.db.WithContext(ctx).
		Table("email_layout_translation").
		Select("html_content").
		Where("entity_id = ? AND locale = ?", layoutID, locale).
		Where("html_content IS NOT NULL").
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return row.HTMLContent, nil
}

type layoutContentRow struct {
	LayoutID       string  `gorm:"column:entity_id"`
	Locale         string  `gorm:"column:locale"`
	HTMLContent    *string `gorm:"column:html_content"`
	IsSourceLocale bool    `gorm:"column:is_source_locale"`
}

func layoutContents(rows []layoutContentRow) []email.LayoutContent {
	contents := make([]email.LayoutContent, 0, len(rows))
	for _, row := range rows {
		contents = append(contents, email.LayoutContent{
			LayoutID: row.LayoutID, Locale: row.Locale, HTMLContent: row.HTMLContent, IsSourceLocale: row.IsSourceLocale,
		})
	}
	return contents
}

func (s renderingStore) LayoutTranslations(ctx context.Context, layoutID string, locales []string) ([]email.LayoutContent, error) {
	var rows []layoutContentRow
	if err := s.db.WithContext(ctx).Table("email_layout_translation").
		Select("entity_id, locale, html_content").
		Where("entity_id = ? AND locale IN ?", layoutID, locales).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return layoutContents(rows), nil
}

func (s renderingStore) SourceLayouts(ctx context.Context, layoutIDs []string) ([]email.LayoutContent, error) {
	var rows []layoutContentRow
	if err := s.db.WithContext(ctx).
		Table("email_layout_translation AS layout").
		Select("layout.entity_id, layout.locale, layout.html_content, TRUE AS is_source_locale").
		Joins("JOIN email_layout AS root ON root.id = layout.entity_id AND root.source_locale = layout.locale").
		Where("layout.entity_id IN ?", layoutIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return layoutContents(rows), nil
}

func (s renderingStore) LayoutLocales(ctx context.Context, layoutID string) ([]email.LayoutContent, error) {
	var rows []layoutContentRow
	if err := s.db.WithContext(ctx).
		Table("email_layout_translation AS entry").
		Select("entry.entity_id, entry.locale, entry.html_content, entry.locale = root.source_locale AS is_source_locale").
		Joins("JOIN email_layout AS root ON root.id = entry.entity_id").
		Where("entry.entity_id = ?", layoutID).
		Order("is_source_locale DESC, locale ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return layoutContents(rows), nil
}

type layoutEntryRow struct {
	Locale      string         `gorm:"column:locale"`
	ContentHTML sql.NullString `gorm:"column:html_content"`
	ContentText sql.NullString `gorm:"column:content_text"`
	UpdatedAt   time.Time      `gorm:"column:updated_at"`
}

func nullable(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func (row layoutEntryRow) entry() email.LayoutEntry {
	return email.LayoutEntry{
		Locale: row.Locale, HTMLContent: nullable(row.ContentHTML), ContentText: nullable(row.ContentText), UpdatedAt: row.UpdatedAt,
	}
}

func (s renderingStore) LayoutEntry(ctx context.Context, layoutID, locale string) (*email.LayoutEntry, error) {
	var row layoutEntryRow
	result := s.db.WithContext(ctx).Raw(
		`SELECT locale, html_content, content_text, updated_at
		 FROM email_layout_translation
		 WHERE entity_id = ? AND locale = ?
		 LIMIT 1`,
		layoutID,
		locale,
	).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	entry := row.entry()
	return &entry, nil
}

func (s renderingStore) LayoutTargets(ctx context.Context, layoutID, sourceLocale string) ([]email.LayoutEntry, error) {
	var rows []layoutEntryRow
	if err := s.db.WithContext(ctx).Table("email_layout_translation").
		Select("locale, html_content, content_text, updated_at").
		Where("entity_id = ? AND locale <> ?", layoutID, sourceLocale).
		Order("locale ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	entries := make([]email.LayoutEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, row.entry())
	}
	return entries, nil
}

func (s renderingStore) UpsertLayoutEntry(ctx context.Context, layoutID, locale string, html, text *string, now time.Time) error {
	return s.db.WithContext(ctx).Exec(
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
		html,
		text,
		now,
		now,
	).Error
}

func (s renderingStore) UpdateLayoutEntry(ctx context.Context, layoutID, locale string, html, text *string) error {
	return s.db.WithContext(ctx).Exec(
		`UPDATE email_layout_translation
		 SET html_content = ?, content_text = ?
		 WHERE entity_id = ? AND locale = ?`,
		html,
		text,
		layoutID,
		locale,
	).Error
}

func (s renderingStore) InsertLayoutEntry(ctx context.Context, layoutID, locale string, html, text *string, now time.Time) error {
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO email_layout_translation (
			entity_id, locale, html_content, content_text, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO NOTHING`,
		layoutID, locale, html, text, now, now,
	).Error
}
