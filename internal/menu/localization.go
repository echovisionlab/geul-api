package menu

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/localization"
)

// NormalizeItemLocalizationMode resolves the effective localization mode for
// one Menu item. A valid fixed locale on legacy rows implies fixed-locale mode.
func NormalizeItemLocalizationMode(mode *string, fixedLocale *string) string {
	if mode != nil {
		switch strings.TrimSpace(*mode) {
		case LocalizationFixedLocale:
			return LocalizationFixedLocale
		case "", LocalizationTranslated:
			return LocalizationTranslated
		}
	}

	if NormalizeItemFixedLocale(fixedLocale) != nil {
		return LocalizationFixedLocale
	}

	return LocalizationTranslated
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
	case LocalizationTranslated:
		normalizedMode := LocalizationTranslated
		return &normalizedMode, nil
	case LocalizationFixedLocale:
		if normalizedLocale == nil {
			return nil, nil
		}
		normalizedMode := LocalizationFixedLocale
		return &normalizedMode, normalizedLocale
	case "":
		if normalizedLocale == nil {
			return nil, nil
		}
		normalizedMode := LocalizationFixedLocale
		return &normalizedMode, normalizedLocale
	default:
		return nil, nil
	}
}

// ShouldTranslateItemLabel reports whether the label of a Menu item with
// localizationMode and fixedLocale belongs to the requested translation
// target. Fixed-locale labels are emitted only for their own locale.
func ShouldTranslateItemLabel(localizationMode, fixedLocale *string, targetLocale string) bool {
	if NormalizeItemLocalizationMode(localizationMode, fixedLocale) != LocalizationFixedLocale {
		return true
	}

	fixed := NormalizeItemFixedLocale(fixedLocale)
	normalizedTarget := localization.NormalizeSupportedLocale(targetLocale)
	return fixed != nil && normalizedTarget != nil && *fixed == *normalizedTarget
}

// ownsLabel reports whether item's label belongs to locale.
func (item *Item) ownsLabel(locale string) bool {
	return item != nil && ShouldTranslateItemLabel(item.LocalizationMode, item.FixedLocale, locale)
}
