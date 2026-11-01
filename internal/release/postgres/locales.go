package releasepostgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func (s *store) LocaleMetadata(ctx context.Context, releaseID, locale string, forUpdate bool) (release.LocaleMetadata, bool, error) {
	query := s.tx.WithContext(ctx).Table("release_translation").
		Select("locale", "title", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", releaseID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		Locale    string    `gorm:"column:locale"`
		Title     *string   `gorm:"column:title"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return release.LocaleMetadata{}, false, nil
	}
	if err != nil {
		return release.LocaleMetadata{}, false, err
	}
	return release.LocaleMetadata{Locale: row.Locale, Title: row.Title, UpdatedAt: row.UpdatedAt}, true, nil
}

func (s *store) LocaleTitle(ctx context.Context, releaseID, locale string) (*string, error) {
	var row struct {
		Title *string `gorm:"column:title"`
	}
	if err := s.tx.WithContext(ctx).
		Table("release_translation").
		Select("title").
		Where("entity_id = ? AND locale = ?", releaseID, locale).
		Take(&row).Error; err != nil {
		return nil, notFoundAs(err, release.ErrLocaleNotFound)
	}
	return row.Title, nil
}

func (s *store) SaveSourceLocaleTitle(ctx context.Context, releaseID, locale, title string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO release_translation (
		 entity_id, locale, title, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
		 title = EXCLUDED.title,
		 updated_at = EXCLUDED.updated_at`,
		releaseID, locale, title,
		now, now,
	).Error
}

func (s *store) InsertLocale(ctx context.Context, releaseID, locale string, title *string, now time.Time) error {
	created := s.tx.WithContext(ctx).Exec(
		"INSERT INTO release_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, ?, ?, ?, ?)",
		releaseID, locale, title, now, now,
	)
	if created.Error != nil {
		return created.Error
	}
	if created.RowsAffected != 1 {
		return errors.New("Release target locale could not be created")
	}
	return nil
}

func (s *store) UpdateLocaleTitle(ctx context.Context, releaseID, locale string, title *string, updatedAt time.Time) (bool, error) {
	updated := s.tx.WithContext(ctx).Table("release_translation").
		Where("entity_id = ?::uuid AND locale = ?", releaseID, locale).
		Updates(map[string]any{"title": title, "updated_at": updatedAt})
	if updated.Error != nil {
		return false, updated.Error
	}
	return updated.RowsAffected == 1, nil
}

func (s *store) DeleteLocale(ctx context.Context, releaseID, locale string) (bool, error) {
	if err := s.tx.WithContext(ctx).Exec("DELETE FROM release_credit_locale AS localized USING release_credit AS credit WHERE localized.credit_id = credit.id AND credit.release_id = ?::uuid AND localized.locale = ?", releaseID, locale).Error; err != nil {
		return false, err
	}
	result := s.tx.WithContext(ctx).Exec("DELETE FROM release_translation WHERE entity_id = ?::uuid AND locale = ?", releaseID, locale)
	return result.RowsAffected != 0, result.Error
}

type releaseCreditLocaleRow struct {
	CreditID  string    `gorm:"column:credit_id;type:uuid;primaryKey"`
	Locale    string    `gorm:"column:locale;primaryKey"`
	Note      string    `gorm:"column:note"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (releaseCreditLocaleRow) TableName() string { return "release_credit_locale" }

func (s *store) CreditNotes(ctx context.Context, releaseID, locale string) (map[string]string, error) {
	return CreditNotes(ctx, s.tx, releaseID, locale)
}

// CreditNotes maps each credit of the Release with a note in locale to it,
// for the public Release reads.
func CreditNotes(ctx context.Context, db *gorm.DB, releaseID, locale string) (map[string]string, error) {
	var rows []releaseCreditLocaleRow
	if err := db.WithContext(ctx).
		Table("release_credit_locale AS localized").
		Select("localized.credit_id, localized.locale, localized.note").
		Joins("JOIN release_credit AS credit ON credit.id = localized.credit_id").
		Where("credit.release_id = ? AND localized.locale = ?", releaseID, locale).
		Order("localized.credit_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	notes := make(map[string]string, len(rows))
	for _, row := range rows {
		notes[row.CreditID] = row.Note
	}
	return notes, nil
}

func (s *store) ReplaceCreditNotes(ctx context.Context, releaseID, locale string, notes map[string]string, now time.Time) error {
	if err := s.tx.WithContext(ctx).Exec(`
		DELETE FROM release_credit_locale AS localized
		USING release_credit AS credit
		WHERE localized.credit_id = credit.id
		  AND credit.release_id = ?::uuid
		  AND localized.locale = ?
	`, releaseID, locale).Error; err != nil {
		return err
	}
	creditIDs := make([]string, 0, len(notes))
	for creditID := range notes {
		creditIDs = append(creditIDs, creditID)
	}
	sort.Strings(creditIDs)
	for _, creditID := range creditIDs {
		row := releaseCreditLocaleRow{
			CreditID:  creditID,
			Locale:    locale,
			Note:      strings.TrimSpace(notes[creditID]),
			UpdatedAt: now,
		}
		if err := s.tx.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *store) TranslationDefaultLocale(ctx context.Context) (string, error) {
	settings, err := translation.LoadRuntimeSettings(ctx, s.tx)
	if err != nil {
		return "", err
	}
	return settings.DefaultLocale, nil
}
