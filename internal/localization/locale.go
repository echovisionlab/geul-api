package localization

import (
	"strings"

	"golang.org/x/text/language"
)

// Canonical locale identifiers owned by this package. The supported registry
// below is the only source for accepted locale values.
const (
	LocaleEnglish             = "en"
	LocaleKorean              = "ko"
	LocaleJapanese            = "ja"
	LocaleChineseSimplified   = "zh-CN"
	LocaleChineseTraditional  = "zh-TW"
	LocaleSpanish             = "es"
	LocaleSpanishLatinAmerica = "es-419"
	LocaleFrench              = "fr"
	LocaleGerman              = "de"
	LocalePortugueseBrazil    = "pt-BR"
	LocalePortuguesePortugal  = "pt-PT"
	LocaleItalian             = "it"
	LocaleDutch               = "nl"
	LocaleArabic              = "ar"
	LocaleIndonesian          = "id"
	LocaleVietnamese          = "vi"
	LocaleThai                = "th"
	LocaleTurkish             = "tr"
	LocalePolish              = "pl"
	LocaleRussian             = "ru"
)

// canonicalLocaleCodes is the single ordered registry of locale identifiers
// understood by the API. Runtime enablement, public visibility, and
// machine-translation policy remain database-owned translation_locale facts.
var canonicalLocaleCodes = [...]string{
	LocaleEnglish,
	LocaleKorean,
	LocaleJapanese,
	LocaleChineseSimplified,
	LocaleChineseTraditional,
	LocaleSpanish,
	LocaleSpanishLatinAmerica,
	LocaleFrench,
	LocaleGerman,
	LocalePortugueseBrazil,
	LocalePortuguesePortugal,
	LocaleItalian,
	LocaleDutch,
	LocaleArabic,
	LocaleIndonesian,
	LocaleVietnamese,
	LocaleThai,
	LocaleTurkish,
	LocalePolish,
	LocaleRussian,
}

var canonicalLocaleByFoldedCode = func() map[string]string {
	locales := make(map[string]string, len(canonicalLocaleCodes))
	for _, locale := range canonicalLocaleCodes {
		locales[strings.ToLower(locale)] = locale
	}
	return locales
}()

// CanonicalLocaleCodes returns the complete ordered locale registry. Callers
// receive a copy so the package remains the only authority that can mutate it.
func CanonicalLocaleCodes() []string {
	return append([]string(nil), canonicalLocaleCodes[:]...)
}

// NormalizeOptionalSupportedLocale canonicalizes an optional locale. Nil and
// blank values are valid absence; non-blank unsupported values are invalid.
func NormalizeOptionalSupportedLocale(input *string) (string, bool) {
	if input == nil || strings.TrimSpace(*input) == "" {
		return "", true
	}
	locale := NormalizeSupportedLocale(*input)
	if locale == nil {
		return "", false
	}
	return *locale, true
}

// NormalizeSupportedLocale returns Geul's canonical locale for an input tag.
func NormalizeSupportedLocale(input string) *string {
	candidate := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(input, "_", "-")))
	if candidate == "" {
		return nil
	}
	if locale, ok := canonicalLocaleByFoldedCode[candidate]; ok {
		return &locale
	}
	if candidate == "zh" || strings.HasPrefix(candidate, "zh-") {
		locale := LocaleChineseSimplified
		if candidate == "zh-tw" || candidate == "zh-hk" || candidate == "zh-mo" || strings.Contains(candidate, "hant") {
			locale = LocaleChineseTraditional
		}
		return &locale
	}
	if strings.HasPrefix(candidate, "es-") {
		_, region, ok := strings.Cut(candidate, "-")
		if ok && (len(region) == 2 || len(region) == 3) {
			locale := LocaleSpanishLatinAmerica
			if region == "es" {
				locale = LocaleSpanish
			}
			return &locale
		}
	}
	if candidate == "pt-pt" {
		locale := LocalePortuguesePortugal
		return &locale
	}
	if candidate == "pt" || strings.HasPrefix(candidate, "pt-") {
		locale := LocalePortugueseBrazil
		return &locale
	}
	base, _, _ := strings.Cut(candidate, "-")
	if locale, ok := canonicalLocaleByFoldedCode[base]; ok {
		return &locale
	}
	return nil
}

// NormalizeExactSupportedLocale accepts only the byte-exact spelling owned by
// the canonical locale registry. Exact document and room identities must not
// silently trim, fold case, rewrite separators, or resolve regional aliases.
func NormalizeExactSupportedLocale(input string) *string {
	for _, locale := range canonicalLocaleCodes {
		if input == locale {
			canonical := locale
			return &canonical
		}
	}
	return nil
}

// InferPreferredLocaleFromAcceptLanguage returns the first supported locale
// selected from an Accept-Language header.
func InferPreferredLocaleFromAcceptLanguage(headerValue string) *string {
	headerValue = strings.TrimSpace(headerValue)
	if headerValue == "" {
		return nil
	}
	if tags, _, err := language.ParseAcceptLanguage(headerValue); err == nil {
		for _, tag := range tags {
			if locale := NormalizeSupportedLocale(tag.String()); locale != nil {
				return locale
			}
		}
	}
	for segment := range strings.SplitSeq(headerValue, ",") {
		candidate, _, _ := strings.Cut(strings.TrimSpace(segment), ";")
		if locale := NormalizeSupportedLocale(candidate); locale != nil {
			return locale
		}
	}
	return nil
}
