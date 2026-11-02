package menuadapter

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/menu"
	"gorm.io/gorm"
)

// EnsureTranslationLocale stores empty labels for the Menu's locale unless
// that locale already has a row.
func EnsureTranslationLocale(ctx context.Context, db *gorm.DB, menuID, locale string, now time.Time) error {
	empty, err := menu.EncodeTranslationLabelValues(nil)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (entity_id, locale) DO NOTHING`,
		menuID, locale, string(empty), now.UTC(), now.UTC(),
	).Error
}
