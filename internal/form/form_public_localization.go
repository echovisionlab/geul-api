package form

import (
	"context"
	"sort"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"gorm.io/gorm"
)

type formLocalizationRow struct {
	Locale      string
	Title       *string
	ContentJSON []byte
	ContentText *string
	OgAssetID   *string
}

// ResolvePublicLocalization selects the Form locale projection with the
// shared serving policy while keeping all form_translation SQL Form-owned.
func ResolvePublicLocalization(
	ctx context.Context,
	db *gorm.DB,
	formID string,
	acceptLanguage string,
) (LocalizationSelection, error) {
	settings, settingsErr := translation.LoadRuntimeSettings(ctx, db)
	if settingsErr != nil {
		settings = translation.DefaultRuntimeSettings()
	}
	requestedLocale := settings.DefaultLocale
	if inferred := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); inferred != nil {
		requestedLocale = *inferred
	}

	sourceLocale := settings.DefaultLocale
	var rootLocaleRow struct {
		Locale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).Table("form").Select("source_locale").Where("id = ?", formID).Take(&rootLocaleRow).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			return formSourceFallbackSelection(requestedLocale, sourceLocale), err
		}
		return formSourceFallbackSelection(requestedLocale, sourceLocale), errs.NotFound("form", formID)
	}
	if normalized := localization.NormalizeSupportedLocale(rootLocaleRow.Locale); normalized != nil {
		sourceLocale = *normalized
	}

	locales := []string{requestedLocale}
	if requestedLocale != sourceLocale {
		locales = append(locales, sourceLocale)
	}
	rows, err := loadFormLocalizationRows(ctx, db, formID, locales)
	if err != nil {
		return formSourceFallbackSelection(requestedLocale, sourceLocale), err
	}

	sourceRow, hasSourceRow := rows[sourceLocale]
	displayedLocale := sourceLocale
	row := sourceRow
	unitFallback := false
	if targetRow, hasTargetRow := rows[requestedLocale]; requestedLocale != sourceLocale && hasTargetRow {
		localized, overlayFallback, overlayErr := overlayFormLocalizationRow(sourceRow, targetRow)
		if overlayErr != nil {
			return formSourceFallbackSelection(requestedLocale, sourceLocale), overlayErr
		}
		displayedLocale = requestedLocale
		row, unitFallback = localized, overlayFallback
	} else if requestedLocale == sourceLocale {
		displayedLocale = sourceLocale
	}
	selection := LocalizationSelection{
		RequestedLocale: requestedLocale, DisplayedLocale: displayedLocale,
		SourceLocale: sourceLocale, IsFallback: displayedLocale != requestedLocale || unitFallback,
		IsOriginal:       displayedLocale == sourceLocale,
		FallbackReason:   formLocalizationFallbackReason(displayedLocale != requestedLocale || unitFallback),
		AvailableLocales: []string{sourceLocale},
	}
	if hasSourceRow || displayedLocale == requestedLocale {
		selection.Title = row.Title
		selection.ContentJSON = row.ContentJSON
		selection.ContentText = row.ContentText
		selection.OgAssetID = row.OgAssetID
	}

	allRows, availableErr := loadFormLocalizationRows(ctx, db, formID, nil)
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
	return selection, nil
}

func loadFormLocalizationRows(
	ctx context.Context,
	db *gorm.DB,
	formID string,
	locales []string,
) (map[string]formLocalizationRow, error) {
	query := db.WithContext(ctx).
		Table("form_translation AS translation").
		Select(`translation.locale, translation.title, translation.content_json,
			translation.content_text, translation.og_asset_id`).
		Where("translation.entity_id = ?", formID)
	if len(locales) > 0 {
		query = query.Where("locale IN ?", locales)
	}
	var stored []formLocalizationRow
	if err := query.Scan(&stored).Error; err != nil {
		return nil, err
	}
	rows := make(map[string]formLocalizationRow, len(stored))
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

func overlayFormLocalizationRow(source, target formLocalizationRow) (formLocalizationRow, bool, error) {
	result := target
	fallback := false
	if result.Title == nil {
		result.Title = source.Title
		fallback = source.Title != nil
	}
	if result.ContentJSON == nil {
		result.ContentJSON = source.ContentJSON
		fallback = fallback || source.ContentJSON != nil
	} else if len(result.ContentJSON) > 0 && len(source.ContentJSON) > 0 {
		localizedTexts, err := formSchemaTranslationTexts(result.ContentJSON)
		if err != nil {
			return formLocalizationRow{}, false, err
		}
		sourceTexts, err := formSchemaTranslationTexts(source.ContentJSON)
		if err != nil {
			return formLocalizationRow{}, false, err
		}
		for unitID := range sourceTexts {
			if _, exists := localizedTexts[unitID]; !exists {
				fallback = true
				break
			}
		}
		canonical, canonicalText, err := CanonicalizeLocalizedFormSchema(source.ContentJSON, result.ContentJSON)
		if err != nil {
			return formLocalizationRow{}, false, err
		}
		result.ContentJSON = canonical
		if canonicalText != nil {
			result.ContentText = canonicalText
		}
	}
	if result.ContentText == nil {
		result.ContentText = source.ContentText
		fallback = fallback || source.ContentText != nil
	}
	if result.OgAssetID == nil {
		result.OgAssetID = source.OgAssetID
		fallback = fallback || source.OgAssetID != nil
	}
	return result, fallback, nil
}

func formLocalizationFallbackReason(fallback bool) openv1.LocalizationFallbackReason {
	if fallback {
		return openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
	}
	return openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_NONE
}
