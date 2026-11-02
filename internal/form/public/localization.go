package public

import (
	"context"
	"errors"
	"log/slog"
	"sort"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// LocalizationSelection is the locale a public Form read displays, with the
// values of that locale overlaid on the source.
type LocalizationSelection struct {
	RequestedLocale      string
	DisplayedLocale      string
	SourceLocale         string
	AvailableLocales     []string
	IsFallback           bool
	IsOriginal           bool
	FallbackReason       openv1.LocalizationFallbackReason
	Title                *string
	Summary              *string
	ContentJSON          []byte
	ContentHTML          *string
	ContentText          *string
	OgAssetID            *string
	OmitSourceOgFallback bool
}

// resolvePublicLocalization selects the Form locale projection with the
// shared serving policy. The settings and the available locales are best
// effort and read on their own; the selected locales are read in one
// snapshot.
func (s *Service) resolvePublicLocalization(ctx context.Context, formID string, acceptLanguage string) (LocalizationSelection, error) {
	var settings translation.RuntimeSettings
	settingsErr := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		settings, err = store.RuntimeSettings(ctx)
		return err
	})
	if settingsErr != nil {
		slog.ErrorContext(ctx, "Translation settings load failed; serving default locale settings", "domain", "form", "event", "form.operation_failed", "operation", "load_localization_settings", "outcome", "failed", failurecause.Attr(settingsErr))
		settings = translation.DefaultRuntimeSettings()
	}
	requestedLocale := settings.DefaultLocale
	if inferred := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); inferred != nil {
		requestedLocale = *inferred
	}

	sourceLocale := settings.DefaultLocale
	policy := localization.ServingPolicy{DefaultLocale: settings.DefaultLocale}
	var rows map[string]form.LocalizationRow
	err := s.read(ctx, func(store Store) error {
		storedSource, err := store.SourceLocale(ctx, formID)
		if err != nil {
			if errors.Is(err, form.ErrFormNotFound) {
				return domainerr.ResourceNotFound("form", formID).WithCause(err)
			}
			return err
		}
		if normalized := localization.NormalizeSupportedLocale(storedSource); normalized != nil {
			sourceLocale = *normalized
		}
		rows, err = loadFormLocalizationRows(ctx, store, formID, localization.FallbackLocales(requestedLocale, sourceLocale, policy))
		return err
	})
	if err != nil {
		return formSourceFallbackSelection(requestedLocale, sourceLocale), err
	}
	storedLocales := make(map[string]struct{}, len(rows))
	for locale := range rows {
		storedLocales[locale] = struct{}{}
	}
	// Select applies requested -> default locale -> source; a non-source
	// decision always names a stored target row.
	decision := localization.Select(requestedLocale, sourceLocale, storedLocales, policy)

	sourceRow, hasSourceRow := rows[sourceLocale]
	displayedLocale := sourceLocale
	row := sourceRow
	unitFallback := false
	if targetRow, hasTargetRow := rows[decision.DisplayedLocale]; decision.DisplayedLocale != sourceLocale && hasTargetRow {
		localized, overlayFallback, overlayErr := form.OverlayLocalizationRow(sourceRow, targetRow)
		if overlayErr != nil {
			return formSourceFallbackSelection(requestedLocale, sourceLocale), overlayErr
		}
		displayedLocale = decision.DisplayedLocale
		row, unitFallback = localized, overlayFallback
	}
	selection := LocalizationSelection{
		RequestedLocale: requestedLocale, DisplayedLocale: displayedLocale,
		SourceLocale: sourceLocale, IsFallback: displayedLocale != requestedLocale || unitFallback,
		IsOriginal:       displayedLocale == sourceLocale,
		FallbackReason:   formLocalizationFallbackReason(displayedLocale != requestedLocale || unitFallback),
		AvailableLocales: []string{sourceLocale},
	}
	if hasSourceRow || displayedLocale != sourceLocale {
		selection.Title = row.Title
		selection.ContentJSON = row.ContentJSON
		selection.ContentText = row.ContentText
		selection.OgAssetID = row.OgAssetID
	}

	var allRows map[string]form.LocalizationRow
	availableErr := s.read(ctx, func(store Store) error {
		var err error
		allRows, err = loadFormLocalizationRows(ctx, store, formID, nil)
		return err
	})
	if availableErr == nil {
		available := []string{sourceLocale}
		seen := map[string]struct{}{sourceLocale: {}}
		for locale := range allRows {
			if _, exists := seen[locale]; exists {
				continue
			}
			seen[locale] = struct{}{}
			available = append(available, locale)
		}
		sort.Strings(available)
		selection.AvailableLocales = available
	}
	if availableErr != nil {
		slog.ErrorContext(ctx, "Form locale list load failed; serving source locale only", "domain", "form", "event", "form.operation_failed", "operation", "load_available_locales", "outcome", "failed", failurecause.Attr(availableErr))
	}
	return selection, nil
}

// loadFormLocalizationRows reads the Form's supported locales of locales,
// every one when it is empty, by normalized locale.
func loadFormLocalizationRows(ctx context.Context, store Store, formID string, locales []string) (map[string]form.LocalizationRow, error) {
	stored, err := store.LocalizationRows(ctx, formID, locales)
	if err != nil {
		return nil, err
	}
	rows := make(map[string]form.LocalizationRow, len(stored))
	for _, row := range stored {
		normalized := localization.NormalizeSupportedLocale(row.Locale)
		if normalized == nil {
			continue
		}
		row.Locale = *normalized
		rows[*normalized] = row
	}
	return rows, nil
}

func formSourceFallbackSelection(requestedLocale, sourceLocale string) LocalizationSelection {
	return LocalizationSelection{
		RequestedLocale: requestedLocale, DisplayedLocale: sourceLocale, SourceLocale: sourceLocale,
		AvailableLocales: []string{sourceLocale}, IsOriginal: true,
		IsFallback:     requestedLocale != sourceLocale,
		FallbackReason: openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE,
	}
}

func formLocalizationFallbackReason(fallback bool) openv1.LocalizationFallbackReason {
	if fallback {
		return openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
	}
	return openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_NONE
}
