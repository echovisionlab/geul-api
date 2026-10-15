package sitevariablemigrate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/email"
	"gorm.io/gorm"
)

type layoutMigrationOwner struct {
	SourceLocale      string
	ContentDocumentID *string
	Locales           map[string]string `gorm:"-"`
	Before            map[string]string `gorm:"-"`
}

func planLayoutBackfills(ctx context.Context, db *gorm.DB, rows []struct{ Row []byte }) (map[string]layoutMigrationOwner, error) {
	owners := map[string]layoutMigrationOwner{}
	for _, raw := range rows {
		var row struct {
			EntityID    string `json:"entity_id"`
			Locale      string
			HTMLContent *string `json:"html_content"`
		}
		if err := json.Unmarshal(raw.Row, &row); err != nil {
			return nil, err
		}
		if row.HTMLContent == nil {
			return nil, fmt.Errorf("layout %s locale %s: missing HTML", row.EntityID, row.Locale)
		}
		owner, ok := owners[row.EntityID]
		if !ok {
			if err := db.WithContext(ctx).Table("email_layout").Select("source_locale, content_document_id").Where("id = ?", row.EntityID).Take(&owner).Error; err != nil {
				return nil, err
			}
			owner.Locales = map[string]string{}
		}
		owner.Locales[row.Locale] = *row.HTMLContent
		owners[row.EntityID] = owner
	}
	for id, owner := range owners {
		owner.Before = owner.Locales
		canonical, err := email.BackfillLayoutMarkers(owner.SourceLocale, owner.Locales)
		if err != nil {
			return nil, fmt.Errorf("layout %s backfill: %w", id, err)
		}
		// Backfill identities before replacing literals so source and every target
		// retain a common graph even when a literal becomes a pure placeholder.
		for locale, html := range canonical {
			canonical[locale] = EmailSource(html)
		}
		owner.Locales = canonical
		owners[id] = owner
	}
	return owners, nil
}
