package release

import (
	"context"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

type releaseCreditLocaleRow struct {
	CreditID  string    `gorm:"column:credit_id;type:uuid;primaryKey"`
	Locale    string    `gorm:"column:locale;primaryKey"`
	Note      string    `gorm:"column:note"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func loadReleaseCreditIDs(ctx context.Context, db *gorm.DB, releaseID string) ([]string, error) {
	var creditIDs []string
	if err := db.WithContext(ctx).
		Table("release_credit").
		Where("release_id = ?", releaseID).
		Order("id ASC").
		Pluck("id", &creditIDs).Error; err != nil {
		return nil, err
	}
	return creditIDs, nil
}

func (releaseCreditLocaleRow) TableName() string { return "release_credit_locale" }

func loadReleaseCreditLocaleNotes(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	locale string,
) (map[string]string, error) {
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

func replaceReleaseCreditLocaleNotes(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
	locale string,
	notes map[string]string,
	now time.Time,
) error {
	if err := tx.WithContext(ctx).Exec(`
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
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func mergeReleaseCreditLocaleNotes(
	source map[string]string,
	localized map[string]string,
	creditIDs []string,
) map[string]string {
	allowed := make(map[string]struct{}, len(creditIDs))
	for _, creditID := range creditIDs {
		allowed[creditID] = struct{}{}
	}
	merged := make(map[string]string, len(source)+len(localized))
	for creditID, note := range source {
		if _, ok := allowed[creditID]; ok && strings.TrimSpace(note) != "" {
			merged[creditID] = strings.TrimSpace(note)
		}
	}
	for creditID, note := range localized {
		if _, ok := allowed[creditID]; ok && strings.TrimSpace(note) != "" {
			merged[creditID] = strings.TrimSpace(note)
		}
	}
	return merged
}

// LoadReleaseCreditNotesForDisplay overlays the selected locale on the
// source-locale relation notes. Credit membership remains owned by
// release_credit; callers cannot surface notes for detached credits.
func LoadReleaseCreditNotesForDisplay(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	sourceLocale string,
	displayedLocale string,
	creditIDs []string,
) (map[string]string, error) {
	source, err := loadReleaseCreditLocaleNotes(ctx, db, releaseID, sourceLocale)
	if err != nil {
		return nil, err
	}
	localized := source
	if displayedLocale != sourceLocale {
		localized, err = loadReleaseCreditLocaleNotes(ctx, db, releaseID, displayedLocale)
		if err != nil {
			return nil, err
		}
	}
	return mergeReleaseCreditLocaleNotes(source, localized, creditIDs), nil
}
