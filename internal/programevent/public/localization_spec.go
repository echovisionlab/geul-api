package public

import (
	"github.com/echovisionlab/geul-api/internal/localization"
	programevent "github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
)

const defaultPublicLocale = localization.LocaleEnglish

var programEventLocalizationSpec = publiccontent.Spec{
	EntityType:   programevent.EntityType,
	TableName:    "program_event_translation",
	SelectClause: "locale, summary, NULL::text AS content_html, NULL::text AS content_text",
}

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
