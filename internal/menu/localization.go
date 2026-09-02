package menu

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
)

// NormalizeItemLocalizationMode resolves the effective localization mode for
// one Menu item. A valid fixed locale on legacy rows implies fixed-locale mode.
func NormalizeItemLocalizationMode(mode *string, fixedLocale *string) string {
	if mode != nil {
		switch strings.TrimSpace(*mode) {
		case model.MenuItemLocalizationModeFixedLocale:
			return model.MenuItemLocalizationModeFixedLocale
		case "", model.MenuItemLocalizationModeTranslated:
			return model.MenuItemLocalizationModeTranslated
		}
	}

	if NormalizeItemFixedLocale(fixedLocale) != nil {
		return model.MenuItemLocalizationModeFixedLocale
	}

	return model.MenuItemLocalizationModeTranslated
}

// NormalizeItemFixedLocale returns the canonical supported locale for a Menu
// item, or nil when the configured value is absent or unsupported.
func NormalizeItemFixedLocale(locale *string) *string {
	if locale == nil {
		return nil
	}
	return localization.NormalizeSupportedLocale(strings.TrimSpace(*locale))
}

// CanonicalizeItemLocalization persists only an explicit, valid Menu
// localization configuration.
func CanonicalizeItemLocalization(mode *string, fixedLocale *string) (*string, *string) {
	normalizedLocale := NormalizeItemFixedLocale(fixedLocale)
	modeValue := ""
	if mode != nil {
		modeValue = strings.TrimSpace(*mode)
	}

	switch modeValue {
	case model.MenuItemLocalizationModeTranslated:
		normalizedMode := model.MenuItemLocalizationModeTranslated
		return &normalizedMode, nil
	case model.MenuItemLocalizationModeFixedLocale:
		if normalizedLocale == nil {
			return nil, nil
		}
		normalizedMode := model.MenuItemLocalizationModeFixedLocale
		return &normalizedMode, normalizedLocale
	case "":
		if normalizedLocale == nil {
			return nil, nil
		}
		normalizedMode := model.MenuItemLocalizationModeFixedLocale
		return &normalizedMode, normalizedLocale
	default:
		return nil, nil
	}
}

// ShouldTranslateItemLabel reports whether a Menu item label belongs to the
// requested translation target. Fixed-locale labels are emitted only for their
// own locale.
func ShouldTranslateItemLabel(item *model.MenuItem, targetLocale string) bool {
	if item == nil {
		return false
	}
	if NormalizeItemLocalizationMode(item.LocalizationMode, item.FixedLocale) !=
		model.MenuItemLocalizationModeFixedLocale {
		return true
	}

	fixedLocale := NormalizeItemFixedLocale(item.FixedLocale)
	normalizedTarget := localization.NormalizeSupportedLocale(targetLocale)
	return fixedLocale != nil && normalizedTarget != nil && *fixedLocale == *normalizedTarget
}
