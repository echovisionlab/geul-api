package programeventpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type localeMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (s *store) InsertSourceLocale(ctx context.Context, eventID, locale string, summary *string, at time.Time) error {
	return s.tx.WithContext(ctx).Create(&model.ProgramEventTranslation{
		EntityID:  eventID,
		Locale:    locale,
		Summary:   summary,
		CreatedAt: at,
		UpdatedAt: at,
	}).Error
}

func (s *store) LocaleMetadata(ctx context.Context, eventID, locale string, forUpdate bool) (programevent.LocaleMetadata, bool, error) {
	query := s.tx.WithContext(ctx).Table("program_event_translation").
		Select("locale", "summary", "updated_at").
		Where("entity_id = ? AND locale = ?", eventID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row localeMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return programevent.LocaleMetadata{}, false, nil
		}
		return programevent.LocaleMetadata{}, false, err
	}
	return programevent.LocaleMetadata{Locale: row.Locale, Summary: row.Summary, UpdatedAt: row.UpdatedAt}, true, nil
}

func (s *store) LockLocale(ctx context.Context, eventID, locale string) (programevent.LocaleMetadata, error) {
	var row model.ProgramEventTranslation
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("entity_id = ? AND locale = ?", eventID, locale).
		Take(&row).Error; err != nil {
		return programevent.LocaleMetadata{}, notFound(err, programevent.ErrLocaleNotFound)
	}
	return programevent.LocaleMetadata{Locale: row.Locale, Summary: row.Summary, UpdatedAt: row.UpdatedAt}, nil
}

func (s *store) SetLocaleSummary(ctx context.Context, eventID, locale string, summary *string, updatedAt time.Time) error {
	return s.tx.Model(&model.ProgramEventTranslation{}).
		Where("entity_id = ? AND locale = ?", eventID, locale).
		Updates(structured.Fields{"summary": summary, "updated_at": updatedAt}).Error
}

func (s *store) InsertTargetLocale(ctx context.Context, eventID, locale string, summary *string, at time.Time) (int64, error) {
	created := s.tx.WithContext(ctx).Exec(
		`INSERT INTO program_event_translation (entity_id, locale, summary, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		eventID, locale, summary, at, at,
	)
	return created.RowsAffected, created.Error
}

func (s *store) UpdateTargetLocale(ctx context.Context, eventID, locale string, summary *string, updatedAt time.Time) (int64, error) {
	updated := s.tx.WithContext(ctx).Table("program_event_translation").
		Where("entity_id = ? AND locale = ?", eventID, locale).
		Updates(map[string]any{"summary": summary, "updated_at": updatedAt})
	return updated.RowsAffected, updated.Error
}

func (s *store) DeleteTargetLocale(ctx context.Context, eventID, locale string) (int64, error) {
	deleted := s.tx.WithContext(ctx).Exec(
		"DELETE FROM program_event_translation WHERE entity_id = ? AND locale = ?", eventID, locale,
	)
	return deleted.RowsAffected, deleted.Error
}

func (s *store) LocaleSummaries(ctx context.Context, eventID string) ([]programevent.LocaleSummary, error) {
	var rows []struct {
		Locale  string  `gorm:"column:locale"`
		Summary *string `gorm:"column:summary"`
	}
	if err := s.tx.WithContext(ctx).Table("program_event_translation AS translation").
		Select("translation.locale", "translation.summary").
		Joins("JOIN program_event AS event ON event.id = translation.entity_id").
		Where("translation.entity_id = ?", eventID).
		Order("translation.locale = event.source_locale DESC, translation.locale ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]programevent.LocaleSummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, programevent.LocaleSummary{Locale: row.Locale, Summary: row.Summary})
	}
	return result, nil
}

func (s *store) LocaleSummary(ctx context.Context, eventID, locale string) (*string, bool, error) {
	var row struct {
		Summary *string `gorm:"column:summary"`
	}
	query := s.tx.WithContext(ctx).Table("program_event_translation").Select("summary").
		Where("entity_id = ? AND locale = ?", eventID, locale).Take(&row)
	if query.Error != nil {
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, query.Error
	}
	return row.Summary, true, nil
}
