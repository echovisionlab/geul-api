package public

import (
	"github.com/echovisionlab/geul-api/internal/localization"
)

const defaultPublicLocale = localization.LocaleEnglish

func resolveRequestedLocale(acceptLanguage string) string {
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	return defaultPublicLocale
}

func normalizeSourceLocale(sourceLocale string) string {
	return normalizeSourceLocaleWithDefault(sourceLocale, defaultPublicLocale)
}

func normalizeSourceLocaleWithDefault(sourceLocale, defaultLocale string) string {
	return localization.NormalizeWithDefault(sourceLocale, defaultLocale)
}
