package legalpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func (s *store) CountTranslations(ctx context.Context, kind legal.PolicyKind, id, locale string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table(translationTable(kind)).
		Where("entity_id = ? AND locale = ?", id, locale).
		Count(&count).Error
	return count, err
}

func (s *store) Translation(ctx context.Context, kind legal.PolicyKind, id, locale string, forUpdate bool) (legal.Translation, bool, error) {
	query := s.tx.WithContext(ctx).Table(translationTable(kind)).
		Select("locale", "title", "updated_at").
		Where("entity_id = ? AND locale = ?", id, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		Locale    string    `gorm:"column:locale"`
		Title     *string   `gorm:"column:title"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legal.Translation{}, false, nil
		}
		return legal.Translation{}, false, err
	}
	return legal.Translation{Locale: row.Locale, Title: row.Title, UpdatedAt: row.UpdatedAt}, true, nil
}

func (s *store) TranslationTitle(ctx context.Context, kind legal.PolicyKind, id, locale string) (*string, error) {
	var row struct {
		Title *string `gorm:"column:title"`
	}
	if err := s.tx.WithContext(ctx).Table(translationTable(kind)).
		Select("title").
		Where("entity_id = ? AND locale = ?", id, locale).
		Take(&row).Error; err != nil {
		return nil, notFound(err, legal.ErrTranslationNotFound)
	}
	return row.Title, nil
}

func (s *store) TranslationLocales(ctx context.Context, kind legal.PolicyKind, id string) ([]string, error) {
	var locales []string
	err := s.tx.WithContext(ctx).Table(translationTable(kind)).
		Where("entity_id = ?", id).
		Pluck("locale", &locales).Error
	return locales, err
}

func (s *store) InsertTranslation(ctx context.Context, kind legal.PolicyKind, id, locale string, title *string, at time.Time) (int64, error) {
	insert := s.tx.WithContext(ctx).Exec(
		"INSERT INTO "+translationTable(kind)+" (entity_id, locale, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		id, locale, title, at, at,
	)
	return insert.RowsAffected, insert.Error
}

func (s *store) UpdateTranslation(ctx context.Context, kind legal.PolicyKind, id, locale string, title *string, at time.Time) (int64, error) {
	updated := s.tx.WithContext(ctx).Table(translationTable(kind)).
		Where("entity_id = ? AND locale = ?", id, locale).
		Updates(structured.Fields{"title": title, "updated_at": at})
	return updated.RowsAffected, updated.Error
}

func (s *store) DeleteTranslation(ctx context.Context, kind legal.PolicyKind, id, locale string) (int64, error) {
	deleted := s.tx.WithContext(ctx).Table(translationTable(kind)).
		Where("entity_id = ? AND locale = ?", id, locale).Delete(nil)
	return deleted.RowsAffected, deleted.Error
}

func (s *store) StoreTargetProjection(ctx context.Context, kind legal.PolicyKind, id, locale string, projection legal.Projection) error {
	return s.tx.WithContext(ctx).Table(translationTable(kind)).
		Where("entity_id = ? AND locale = ?", id, locale).
		Updates(structured.Fields{"content_html": projection.HTML, "content_text": projection.Text}).Error
}
