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

// Select chooses an exact stored target or the source projection without
// depending on target status, source-currentness, quality, or provenance.
func Select(
	requestedLocale string,
	sourceLocale string,
	storedLocales map[string]struct{},
	policy ServingPolicy,
) Decision {
	requestedLocale = NormalizeWithDefault(requestedLocale, policy.DefaultLocale)
	sourceLocale = NormalizeWithDefault(sourceLocale, policy.DefaultLocale)
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
	if _, ok := storedLocales[sourceLocale]; ok {
		applyStoredLocale(&decision, sourceLocale)
	}
	return decision
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
