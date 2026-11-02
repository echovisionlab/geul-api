package legal

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
)

// refreshDerivedContent rebuilds the version's HTML and text from snapshot,
// in the source locale and every locale the snapshot or a translation row
// has.
func refreshDerivedContent(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
	snapshot contentblock.Snapshot,
	sourceLocale string,
	now time.Time,
) error {
	locales, err := derivedContentLocales(ctx, store, kind, entityID, snapshot, sourceLocale)
	if err != nil {
		return err
	}
	var variables sitevariable.Context
	// Existing variable-free fixtures need no settings table. Settings failures
	// still fail closed whenever the canonical document contains references.
	if snapshotHasSiteVariables(snapshot) {
		if variables, err = store.SiteVariables(ctx); err != nil {
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
		derived := Projection{HTML: projection.HTML, Text: projection.Text, Digest: snapshot.SnapshotDigest}
		if locale == sourceLocale {
			err = store.StoreSourceProjection(ctx, kind.kind, entityID, derived, now)
		} else {
			err = store.StoreTargetProjection(ctx, kind.kind, entityID, locale, derived)
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
	store Store,
	kind *policyKind,
	entityID string,
	snapshot contentblock.Snapshot,
	sourceLocale string,
) ([]string, error) {
	persistedLocales, err := store.TranslationLocales(ctx, kind.kind, entityID)
	if err != nil {
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
