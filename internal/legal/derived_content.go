package legal

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// refreshDerivedContent rebuilds the version's HTML and text from snapshot,
// in the source locale and every locale the snapshot or a translation row
// has.
func refreshDerivedContent(
	ctx context.Context,
	tx *gorm.DB,
	kind string,
	entityID string,
	snapshot contentblock.Snapshot,
	sourceLocale string,
	now time.Time,
) error {
	locales, err := derivedContentLocales(ctx, tx, kind, entityID, snapshot, sourceLocale)
	if err != nil {
		return err
	}
	var variables sitevariable.Context
	// Existing variable-free fixtures need no settings table. Settings failures
	// still fail closed whenever the canonical document contains references.
	if snapshotHasSiteVariables(snapshot) {
		if variables, err = sitevariable.LoadRuntime(ctx, tx); err != nil {
			return err
		}
	}
	for _, locale := range locales {
		document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
		if err != nil {
			return err
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, variables)
		if err != nil {
			return err
		}
		if locale == sourceLocale {
			err = tx.WithContext(ctx).Table(kind+"_history").Where("id = ?", entityID).
				Updates(structured.Fields{
					"content":      projection.HTML,
					"content_text": projection.Text,
					"content_hash": snapshot.SnapshotDigest,
					"view_hash":    snapshot.SnapshotDigest,
					"updated_at":   now,
				}).Error
		} else {
			err = tx.WithContext(ctx).Table(kind+"_translation").
				Where("entity_id = ? AND locale = ?", entityID, locale).
				Updates(structured.Fields{"content_html": projection.HTML, "content_text": projection.Text}).Error
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// derivedContentLocales lists the source locale, then each locale of the
// snapshot and of the version's translation rows, once.
func derivedContentLocales(
	ctx context.Context,
	tx *gorm.DB,
	kind string,
	entityID string,
	snapshot contentblock.Snapshot,
	sourceLocale string,
) ([]string, error) {
	var persistedLocales []string
	if err := tx.WithContext(ctx).Table(kind+"_translation").
		Where("entity_id = ?", entityID).
		Pluck("locale", &persistedLocales).Error; err != nil {
		return nil, err
	}
	candidates := append(append([]string{sourceLocale}, legalSnapshotLocales(snapshot)...), persistedLocales...)
	locales := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, locale := range candidates {
		if _, exists := seen[locale]; exists {
			continue
		}
		seen[locale] = struct{}{}
		locales = append(locales, locale)
	}
	return locales, nil
}

func legalSnapshotLocales(snapshot contentblock.Snapshot) []string {
	locales := make([]string, 0, len(snapshot.LocaleOverlays))
	for _, overlay := range snapshot.LocaleOverlays {
		if locale := strings.TrimSpace(overlay.Locale); locale != "" {
			locales = append(locales, locale)
		}
	}
	return locales
}

func snapshotHasSiteVariables(snapshot contentblock.Snapshot) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		for _, block := range overlay.Blocks {
			if strings.Contains(string(block.LocalizedData), `"siteVariable"`) || strings.Contains(string(block.LocalizedData), "{{site.") || strings.Contains(string(block.LocalizedData), "{{contact.") {
				return true
			}
		}
	}
	return false
}
