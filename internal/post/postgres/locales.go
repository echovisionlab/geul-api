package postpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func (s *store) SourceLocaleStates(ctx context.Context, postIDs []string) (map[string]post.SourceLocaleState, error) {
	return SourceLocaleStates(ctx, s.tx, postIDs)
}

// SourceLocaleStates reads the source locale row of each Post that has one,
// for the public Post reads.
func SourceLocaleStates(ctx context.Context, db *gorm.DB, postIDs []string) (map[string]post.SourceLocaleState, error) {
	normalized := sliceutil.UniqueTrimmed(postIDs)
	if len(normalized) == 0 {
		return map[string]post.SourceLocaleState{}, nil
	}
	var rows []struct {
		EntityID  string         `gorm:"column:entity_id"`
		Title     sql.NullString `gorm:"column:title"`
		Summary   sql.NullString `gorm:"column:summary"`
		OgAssetID sql.NullString `gorm:"column:og_asset_id"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT pt.entity_id::text AS entity_id,
		        pt.title,
		        pt.summary,
		        pt.og_asset_id
		 FROM post_translation AS pt
		 JOIN post AS p
		   ON p.id = pt.entity_id AND p.source_locale = pt.locale
		 WHERE pt.entity_id::text IN ?`,
		normalized,
	).Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}
	states := make(map[string]post.SourceLocaleState, len(rows))
	for _, row := range rows {
		var state post.SourceLocaleState
		if row.Title.Valid {
			state.Title = &row.Title.String
		}
		if row.Summary.Valid {
			state.Summary = &row.Summary.String
		}
		if row.OgAssetID.Valid {
			state.OgAssetID = &row.OgAssetID.String
		}
		states[row.EntityID] = state
	}
	return states, nil
}

func (s *store) UpsertSourceLocale(ctx context.Context, postID, locale string, title, summary *string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO post_translation (
			entity_id, locale, title, summary, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			updated_at = EXCLUDED.updated_at`,
		postID,
		locale,
		title,
		summary,
		now,
		now,
	).Error
}

type localeMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (row localeMetadataRow) metadata() post.LocaleMetadata {
	return post.LocaleMetadata{Locale: row.Locale, Title: row.Title, Summary: row.Summary, UpdatedAt: row.UpdatedAt}
}

func (s *store) LocaleMetadata(ctx context.Context, postID, locale string, forUpdate bool) (post.LocaleMetadata, error) {
	db := s.tx
	if forUpdate {
		db = db.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row localeMetadataRow
	if err := db.WithContext(ctx).
		Table("post_translation").
		Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Take(&row).Error; err != nil {
		return post.LocaleMetadata{}, notFound(err, post.ErrLocaleNotFound)
	}
	return row.metadata(), nil
}

func (s *store) OptionalLocaleMetadata(ctx context.Context, postID, locale string, forUpdate bool) (post.LocaleMetadata, bool, error) {
	query := s.tx.WithContext(ctx).Table("post_translation").
		Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", postID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row localeMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return post.LocaleMetadata{}, false, nil
		}
		return post.LocaleMetadata{}, false, err
	}
	return row.metadata(), true, nil
}

func (s *store) LocaleText(ctx context.Context, postID, locale string, forUpdate bool) (post.AIDocumentLocaleMetadata, bool, error) {
	query := s.tx.WithContext(ctx).Table("post_translation")
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		Title   *string `gorm:"column:title"`
		Summary *string `gorm:"column:summary"`
	}
	err := query.
		Select("title", "summary").
		Where("entity_id = ?::uuid AND locale = ?", postID, locale).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return post.AIDocumentLocaleMetadata{}, false, nil
	}
	if err != nil {
		return post.AIDocumentLocaleMetadata{}, false, err
	}
	return post.AIDocumentLocaleMetadata{Title: row.Title, Summary: row.Summary}, true, nil
}

func (s *store) CountLocale(ctx context.Context, postID, locale string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Count(&count).Error
	return count, err
}

func (s *store) UpdateLocaleMetadata(ctx context.Context, postID, locale string, change post.LocaleChange) (int64, error) {
	updates := structured.Fields{}
	if change.SetTitle {
		updates["title"] = change.Title
	}
	if change.SetSummary {
		updates["summary"] = change.Summary
	}
	updates["updated_at"] = change.UpdatedAt
	result := s.tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Updates(updates)
	return result.RowsAffected, result.Error
}

func (s *store) InsertLocale(ctx context.Context, postID, locale string, title, summary *string, now time.Time) (int64, error) {
	insert := s.tx.WithContext(ctx).Exec(
		`INSERT INTO post_translation (
				entity_id, locale, title, summary, created_at, updated_at
			 ) VALUES (?::uuid, ?, ?, ?, ?, ?)`,
		postID, locale, title, summary, now, now,
	)
	return insert.RowsAffected, insert.Error
}

func (s *store) InsertEmptyLocale(ctx context.Context, postID, locale string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO post_translation (
				entity_id, locale, created_at, updated_at
			) VALUES (?::uuid, ?, ?, ?)`,
		postID, locale, now, now,
	).Error
}

func (s *store) UpsertLocaleText(ctx context.Context, postID, locale string, title, summary *string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO post_translation (
		 entity_id, locale, title, summary, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
		 title = EXCLUDED.title,
		 summary = EXCLUDED.summary,
		 updated_at = EXCLUDED.updated_at`,
		postID, locale, title, summary,
		now, now,
	).Error
}

func (s *store) DeleteLocale(ctx context.Context, postID, locale string) (int64, error) {
	deleted := s.tx.WithContext(ctx).Exec(
		"DELETE FROM post_translation WHERE entity_id = ?::uuid AND locale = ?",
		postID, locale,
	)
	return deleted.RowsAffected, deleted.Error
}
