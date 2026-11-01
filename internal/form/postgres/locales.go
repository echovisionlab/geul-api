package formpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func (s *store) Locale(ctx context.Context, formID, locale string, update bool) (form.Locale, bool, error) {
	query := s.tx.WithContext(ctx).Table("form_translation").Select("xmin::text AS revision", "title", "content_json", "content_text", "updated_at").Where("entity_id = ?::uuid AND locale = ?", formID, locale)
	if update {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		Revision    string    `gorm:"column:revision"`
		Title       *string   `gorm:"column:title"`
		Schema      []byte    `gorm:"column:content_json"`
		ContentText *string   `gorm:"column:content_text"`
		UpdatedAt   time.Time `gorm:"column:updated_at"`
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return form.Locale{}, false, nil
	}
	if result.Error != nil {
		return form.Locale{}, false, result.Error
	}
	return form.Locale{
		Revision: row.Revision, Title: row.Title, Schema: row.Schema, ContentText: row.ContentText, UpdatedAt: row.UpdatedAt,
	}, true, nil
}

func (s *store) LocaleState(ctx context.Context, formID, locale string) (form.TranslationDocumentState, error) {
	return LocaleState(ctx, s.tx, formID, locale)
}

// LocaleState reads one locale with its OG image in tx; the public read
// Store shares it.
func LocaleState(ctx context.Context, tx *gorm.DB, formID, locale string) (form.TranslationDocumentState, error) {
	var row struct {
		Title       sql.NullString `gorm:"column:title"`
		ContentJSON []byte         `gorm:"column:content_json"`
		ContentText sql.NullString `gorm:"column:content_text"`
		OgAssetID   sql.NullString `gorm:"column:og_asset_id"`
		UpdatedAt   time.Time      `gorm:"column:updated_at"`
	}
	result := tx.WithContext(ctx).Raw(
		`SELECT title, content_json, content_text, og_asset_id, updated_at
		 FROM form_translation
		 WHERE entity_id = ? AND locale = ?
		 LIMIT 1`,
		formID,
		locale,
	).Scan(&row)
	if result.Error != nil {
		return form.TranslationDocumentState{}, result.Error
	}
	if result.RowsAffected == 0 {
		return form.TranslationDocumentState{}, form.ErrLocaleNotFound
	}

	state := form.TranslationDocumentState{ContentJSON: row.ContentJSON}
	if row.Title.Valid {
		state.Title = &row.Title.String
	}
	if row.ContentText.Valid {
		state.ContentText = &row.ContentText.String
	}
	if row.OgAssetID.Valid {
		state.OgAssetID = &row.OgAssetID.String
	}
	return state, nil
}

func (s *store) LocalizationRows(ctx context.Context, formID string, locales []string) ([]form.LocalizationRow, error) {
	return LocalizationRows(ctx, s.tx, formID, locales)
}

// LocalizationRows reads the Form's locales of locales in tx, every locale
// when it is empty; the public read Store shares it.
func LocalizationRows(ctx context.Context, tx *gorm.DB, formID string, locales []string) ([]form.LocalizationRow, error) {
	query := tx.WithContext(ctx).
		Table("form_translation AS translation").
		Select(`translation.locale, translation.title, translation.content_json,
			translation.content_text, translation.og_asset_id`).
		Where("translation.entity_id = ?", formID)
	if len(locales) > 0 {
		query = query.Where("locale IN ?", locales)
	}
	var stored []struct {
		Locale      string
		Title       *string
		ContentJSON []byte
		ContentText *string
		OgAssetID   *string
	}
	if err := query.Scan(&stored).Error; err != nil {
		return nil, err
	}
	rows := make([]form.LocalizationRow, len(stored))
	for i, row := range stored {
		rows[i] = form.LocalizationRow{
			Locale: row.Locale, Title: row.Title, ContentJSON: row.ContentJSON, ContentText: row.ContentText, OgAssetID: row.OgAssetID,
		}
	}
	return rows, nil
}

func (s *store) SaveSourceLocale(ctx context.Context, formID, locale string, title *string, schema []byte, contentText *string, now time.Time) error {
	var titleValue structured.Value
	if title != nil {
		titleValue = *title
	}
	schemaJSONValue := structured.Value(nil)
	if schema != nil {
		schemaJSONValue = schema
	}
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO form_translation (
			entity_id, locale, title, content_json, content_text,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = COALESCE(EXCLUDED.title, form_translation.title),
			content_json = COALESCE(EXCLUDED.content_json, form_translation.content_json),
			content_text = EXCLUDED.content_text,
			updated_at = EXCLUDED.updated_at`,
		formID,
		locale,
		titleValue,
		schemaJSONValue,
		contentText,
		now,
		now,
	).Error
}

func (s *store) CreateEmptyLocale(ctx context.Context, formID, locale string, schema []byte, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Exec(
		`INSERT INTO form_translation (
			entity_id, locale, title, content_json, content_text, created_at, updated_at
		) VALUES (?::uuid, ?, NULL, CAST(? AS jsonb), NULL, ?, ?)
		ON CONFLICT (entity_id, locale) DO NOTHING`,
		formID,
		locale,
		string(schema),
		now,
		now,
	)
	return result.RowsAffected != 0, result.Error
}

func (s *store) InsertLocale(ctx context.Context, formID, locale string, change form.LocaleChange, createdAt time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO form_translation (
			entity_id, locale, title, content_json, content_text, created_at, updated_at
		) VALUES (?::uuid, ?, ?, ?, ?, ?, ?)`,
		formID, locale, change.Title, jsonValueOrNil(change.Schema), change.ContentText, createdAt, change.UpdatedAt,
	).Error
}

func (s *store) UpdateLocale(ctx context.Context, formID, locale string, change form.LocaleChange) (bool, error) {
	columns := structured.Fields{"updated_at": change.UpdatedAt}
	if change.SetTitle {
		columns["title"] = change.Title
	}
	if change.SetSchema {
		columns["content_json"] = jsonValueOrNil(change.Schema)
		columns["content_text"] = change.ContentText
	}
	result := s.tx.WithContext(ctx).Table("form_translation").
		Where("entity_id = ?::uuid AND locale = ?", formID, locale).
		Updates(columns)
	return result.RowsAffected != 0, result.Error
}

func (s *store) DeleteLocale(ctx context.Context, formID, locale string) (bool, error) {
	result := s.tx.WithContext(ctx).Exec("DELETE FROM form_translation WHERE entity_id = ?::uuid AND locale = ?", formID, locale)
	return result.RowsAffected != 0, result.Error
}

// jsonValueOrNil is a JSON column value: NULL for an empty document.
func jsonValueOrNil(value []byte) structured.Value {
	if len(value) == 0 {
		return nil
	}
	return value
}
