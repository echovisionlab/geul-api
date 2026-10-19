package localization

import "slices"

// FallbackReason explains why the displayed locale differs from an exact,
// stored target locale.
type FallbackReason uint8

const (
	FallbackNone FallbackReason = iota
	FallbackSource
)

// ServingPolicy controls public localized-projection selection. It contains no
// entity storage or Translation job policy.
type ServingPolicy struct {
	DefaultLocale string
}

// Decision is the locale-only result of localized projection selection. When
// HasCandidate is true, the owning projection applies the row at
// DisplayedLocale; otherwise it retains its canonical source value.
type Decision struct {
	RequestedLocale string
	DisplayedLocale string
	SourceLocale    string
	IsFallback      bool
	IsOriginal      bool
	FallbackReason  FallbackReason
	HasCandidate    bool
}

// NormalizeWithDefault returns the first canonical locale among value,
// defaultLocale, and English.
func NormalizeWithDefault(value, defaultLocale string) string {
	if locale := NormalizeSupportedLocale(value); locale != nil {
		return *locale
	}
	if locale := NormalizeSupportedLocale(defaultLocale); locale != nil {
		return *locale
	}
	return LocaleEnglish
}

// Select chooses, in order, the exact stored requested locale, the stored
// default-locale row, and finally the source projection, without depending on
// target status, source-currentness, quality, or provenance.
//
// Both fallback steps report FallbackSource: the public contract has no
// separate default-locale reason, so clients tell them apart by comparing
// DisplayedLocale with SourceLocale.
func Select(
	requestedLocale string,
	sourceLocale string,
	storedLocales map[string]struct{},
	policy ServingPolicy,
) Decision {
	requestedLocale = NormalizeWithDefault(requestedLocale, policy.DefaultLocale)
	sourceLocale = NormalizeWithDefault(sourceLocale, policy.DefaultLocale)
	defaultLocale := NormalizeWithDefault(policy.DefaultLocale, LocaleEnglish)
	decision := Decision{
		RequestedLocale: requestedLocale,
		DisplayedLocale: sourceLocale,
		SourceLocale:    sourceLocale,
		IsOriginal:      true,
	}

	if requestedLocale == sourceLocale {
		if _, ok := storedLocales[sourceLocale]; ok {
			applyStoredLocale(&decision, sourceLocale)
		}
		return decision
	}

	if _, ok := storedLocales[requestedLocale]; ok {
		applyStoredLocale(&decision, requestedLocale)
		decision.IsOriginal = false
		return decision
	}

	decision.IsFallback = true
	decision.FallbackReason = FallbackSource
	if defaultLocale != requestedLocale && defaultLocale != sourceLocale {
		if _, ok := storedLocales[defaultLocale]; ok {
			applyStoredLocale(&decision, defaultLocale)
			decision.IsOriginal = false
			return decision
		}
	}
	if _, ok := storedLocales[sourceLocale]; ok {
		applyStoredLocale(&decision, sourceLocale)
	}
	return decision
}

// FallbackLocales returns the locales whose stored rows Select may display for
// requestedLocale: the requested locale, then the default locale, then the
// source, canonicalized and without duplicates. Callers that load a bounded
// set of rows use it so the default-locale fallback row is always present.
func FallbackLocales(requestedLocale, sourceLocale string, policy ServingPolicy) []string {
	requestedLocale = NormalizeWithDefault(requestedLocale, policy.DefaultLocale)
	sourceLocale = NormalizeWithDefault(sourceLocale, policy.DefaultLocale)
	if requestedLocale == sourceLocale {
		return []string{sourceLocale}
	}
	result := []string{requestedLocale}
	if defaultLocale := NormalizeWithDefault(policy.DefaultLocale, LocaleEnglish); !slices.Contains(result, defaultLocale) {
		result = append(result, defaultLocale)
	}
	if !slices.Contains(result, sourceLocale) {
		result = append(result, sourceLocale)
	}
	return result
}

// AvailableLocales returns the canonical source followed by every canonical
// stored target locale. A stored row is available even when every value is
// explicitly empty.
func AvailableLocales(sourceLocale string, storedLocales []string, policy ServingPolicy) []string {
	sourceLocale = NormalizeWithDefault(sourceLocale, policy.DefaultLocale)
	available := []string{sourceLocale}
	seen := map[string]struct{}{sourceLocale: {}}
	for _, storedLocale := range storedLocales {
		normalized := NormalizeSupportedLocale(storedLocale)
		if normalized == nil || *normalized == sourceLocale {
			continue
		}
		if _, exists := seen[*normalized]; exists {
			continue
		}
		seen[*normalized] = struct{}{}
		available = append(available, *normalized)
	}
	if len(available) > 1 {
		slices.Sort(available[1:])
	}
	return available
}

func applyStoredLocale(decision *Decision, locale string) {
	decision.DisplayedLocale = locale
	decision.HasCandidate = true
}
