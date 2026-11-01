package workpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/work"
)

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

type sourceMetadataRow struct {
	EntityID string         `gorm:"column:entity_id"`
	Locale   string         `gorm:"column:locale"`
	Title    sql.NullString `gorm:"column:title"`
	Summary  sql.NullString `gorm:"column:summary"`
}

func (s *store) SourceMetadata(ctx context.Context, workIDs []string) (map[string]work.LocaleMetadata, error) {
	normalized := sliceutil.UniqueTrimmed(workIDs)
	if len(normalized) == 0 {
		return map[string]work.LocaleMetadata{}, nil
	}

	var rows []sourceMetadataRow
	result := s.tx.WithContext(ctx).Raw(`
		SELECT wt.entity_id::text AS entity_id,
		       wt.locale,
		       wt.title,
		       wt.summary
		FROM work_translation AS wt
		JOIN work AS w ON w.id = wt.entity_id
		 AND w.source_locale = wt.locale
		WHERE wt.entity_id::text IN ?
	`, normalized).Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}

	states := make(map[string]work.LocaleMetadata, len(rows))
	for _, row := range rows {
		states[row.EntityID] = work.LocaleMetadata{Locale: row.Locale, Title: nullableString(row.Title), Summary: nullableString(row.Summary)}
	}
	return states, nil
}

type sourceDocumentRow struct {
	EntityID  string         `gorm:"column:entity_id"`
	Title     sql.NullString `gorm:"column:title"`
	Summary   sql.NullString `gorm:"column:summary"`
	OgAssetID sql.NullString `gorm:"column:og_asset_id"`
}

func (s *store) SourceDocuments(ctx context.Context, workIDs []string) (map[string]work.SourceDocument, error) {
	normalized := sliceutil.UniqueTrimmed(workIDs)
	if len(normalized) == 0 {
		return map[string]work.SourceDocument{}, nil
	}

	var rows []sourceDocumentRow
	result := s.tx.WithContext(ctx).Raw(
		`SELECT wt.entity_id::text AS entity_id,
		        wt.title,
		        wt.summary,
		        wt.og_asset_id::text AS og_asset_id
		 FROM work_translation AS wt
		 JOIN work AS source
		   ON source.id = wt.entity_id AND source.source_locale = wt.locale
		 WHERE wt.entity_id::text IN ?`,
		normalized,
	).Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}

	states := make(map[string]work.SourceDocument, len(rows))
	for _, row := range rows {
		states[row.EntityID] = work.SourceDocument{
			Title: nullableString(row.Title), Summary: nullableString(row.Summary), OgAssetID: nullableString(row.OgAssetID),
		}
	}
	return states, nil
}

type localeMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (s *store) LocaleMetadata(ctx context.Context, workID, locale string, forUpdate bool) (work.LocaleMetadata, bool, error) {
	query := s.tx.WithContext(ctx).Table("work_translation").Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", workID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row localeMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return work.LocaleMetadata{}, false, nil
		}
		return work.LocaleMetadata{}, false, err
	}
	return work.LocaleMetadata{Locale: row.Locale, Title: row.Title, Summary: row.Summary, UpdatedAt: row.UpdatedAt}, true, nil
}

type localeTextRow struct {
	Title   sql.NullString `gorm:"column:title"`
	Summary sql.NullString `gorm:"column:summary"`
}

func (s *store) LockLocaleText(ctx context.Context, workID, locale string) (work.LocaleMetadata, error) {
	var row localeTextRow
	result := s.tx.WithContext(ctx).Raw(`
		SELECT title, summary
		FROM work_translation
		WHERE entity_id = ? AND locale = ?
		FOR UPDATE
	`, workID, locale).Scan(&row)
	if result.Error != nil {
		return work.LocaleMetadata{}, result.Error
	}
	if result.RowsAffected == 0 {
		return work.LocaleMetadata{}, work.ErrLocaleNotFound
	}
	return work.LocaleMetadata{Locale: locale, Title: nullableString(row.Title), Summary: nullableString(row.Summary)}, nil
}

func (s *store) LockedLocaleText(ctx context.Context, workID, locale string) (work.LocaleMetadata, bool, error) {
	var row localeTextRow
	result := s.tx.WithContext(ctx).
		Table("work_translation").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("title", "summary").
		Where("entity_id = ? AND locale = ?", workID, locale).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return work.LocaleMetadata{}, false, nil
	}
	if result.Error != nil {
		return work.LocaleMetadata{}, false, result.Error
	}
	return work.LocaleMetadata{Locale: locale, Title: nullableString(row.Title), Summary: nullableString(row.Summary)}, true, nil
}

func (s *store) LocaleUpdatedAt(ctx context.Context, workID, locale string) (time.Time, error) {
	var row struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	result := s.tx.WithContext(ctx).Table("work_translation").Select("updated_at").
		Where("entity_id = ? AND locale = ?", workID, locale).Take(&row)
	if result.Error != nil {
		return time.Time{}, notFound(result.Error, work.ErrLocaleNotFound)
	}
	return row.UpdatedAt, nil
}

func (s *store) InsertSourceLocale(ctx context.Context, workID, locale, title string, summary *string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
		INSERT INTO work_translation (
			entity_id,
			locale,
			title,
			summary,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`,
		workID,
		locale,
		title,
		summary,
		now,
		now,
	).Error
}

func (s *store) UpsertSourceLocale(ctx context.Context, workID, locale string, title, summary *string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
		INSERT INTO work_translation (entity_id, locale, title, summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title, summary = EXCLUDED.summary, updated_at = EXCLUDED.updated_at
	`, workID, locale, title, summary, now, now).Error
}

func (s *store) UpdateLocaleText(ctx context.Context, workID, locale string, change work.LocaleTextChange) error {
	updates := structured.Fields{"updated_at": change.UpdatedAt}
	if change.SetTitle {
		updates["title"] = optionalText(change.Title)
	}
	if change.SetSummary {
		updates["summary"] = change.Summary
	}
	update := s.tx.WithContext(ctx).
		Table("work_translation").
		Where("entity_id = ? AND locale = ?", workID, locale).
		Updates(updates)
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return rowChanged
	}
	return nil
}

// optionalText stores a set title as text, as the locale writes always
// did.
func optionalText(value *string) any {
	if value == nil {
		return (*string)(nil)
	}
	return *value
}

func (s *store) RestoreLocaleText(ctx context.Context, workID, locale string, title, summary *string, now time.Time) error {
	result := s.tx.WithContext(ctx).
		Table("work_translation").
		Where(
			"entity_id = ? AND locale = ?",
			workID,
			locale,
		).
		Updates(structured.Fields{
			"title":      title,
			"summary":    summary,
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return rowChanged
	}
	return nil
}

func (s *store) InsertTargetLocale(ctx context.Context, workID string, metadata work.LocaleMetadata) error {
	created := s.tx.WithContext(ctx).Exec(
		"INSERT INTO work_translation (entity_id, locale, title, summary, created_at, updated_at) VALUES (?::uuid, ?, ?, ?, ?, ?)",
		workID, metadata.Locale, metadata.Title, metadata.Summary, metadata.UpdatedAt, metadata.UpdatedAt,
	)
	if created.Error != nil {
		return created.Error
	}
	if created.RowsAffected != 1 {
		return rowChanged
	}
	return nil
}

func (s *store) UpdateTargetLocale(ctx context.Context, workID, locale string, change work.LocaleTextChange) error {
	updates := map[string]any{}
	if change.SetTitle {
		updates["title"] = change.Title
	}
	if change.SetSummary {
		updates["summary"] = change.Summary
	}
	updates["updated_at"] = change.UpdatedAt
	updated := s.tx.WithContext(ctx).Table("work_translation").Where("entity_id = ?::uuid AND locale = ?", workID, locale).Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return rowChanged
	}
	return nil
}

func (s *store) DeleteTargetLocale(ctx context.Context, workID, locale string) error {
	deleted := s.tx.WithContext(ctx).Exec("DELETE FROM work_translation WHERE entity_id = ?::uuid AND locale = ?", workID, locale)
	if deleted.Error != nil {
		return deleted.Error
	}
	if deleted.RowsAffected != 1 {
		return rowChanged
	}
	return nil
}
