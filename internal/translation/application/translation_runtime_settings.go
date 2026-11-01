package application

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
)

type translationRuntimeSettings = translation.RuntimeSettings

func normalizeTranslationRuntimeSettings(input translationRuntimeSettings) (translationRuntimeSettings, error) {
	return translation.NormalizeRuntimeSettings(input)
}

func applyTranslationRuntimeSettingsUpdate(
	current translationRuntimeSettings,
	request UpdateTranslationSettingsCommand,
) (translationRuntimeSettings, error) {
	if request.Settings == nil {
		return translationRuntimeSettings{}, fmt.Errorf("translation settings are required")
	}
	if request.UpdateMask == nil {
		return translationRuntimeSettings{}, fmt.Errorf("update_mask is required")
	}
	if len(request.UpdateMask.Paths) == 0 {
		return translationRuntimeSettings{}, fmt.Errorf("update_mask must include at least one settings field")
	}

	updated := translationRuntimeSettings{
		DefaultLocale:  current.DefaultLocale,
		ProtectedTerms: append([]string(nil), current.ProtectedTerms...),
		UpdatedAt:      current.UpdatedAt,
	}
	seen := make(map[string]struct{}, len(request.UpdateMask.Paths))
	for _, path := range request.UpdateMask.Paths {
		if _, duplicate := seen[path]; duplicate {
			return translationRuntimeSettings{}, fmt.Errorf("update_mask contains duplicate path %q", path)
		}
		seen[path] = struct{}{}
		switch path {
		case "default_locale":
			parsed, err := normalizeTranslationRuntimeSettings(translationRuntimeSettings{
				DefaultLocale: strings.TrimSpace(request.Settings.DefaultLocale),
			})
			if err != nil {
				return translationRuntimeSettings{}, fmt.Errorf("default_locale: %w", err)
			}
			updated.DefaultLocale = parsed.DefaultLocale
		case "protected_terms":
			if request.BaseSettings == nil {
				return translationRuntimeSettings{}, fmt.Errorf("base_settings are required when patching protected_terms")
			}
			updated.ProtectedTerms = mergeProtectedTermsDelta(
				current.ProtectedTerms,
				request.BaseSettings.ProtectedTerms,
				request.Settings.ProtectedTerms,
			)
		default:
			return translationRuntimeSettings{}, fmt.Errorf("unsupported update_mask path %q", path)
		}
	}
	return updated, nil
}

func mergeProtectedTermsDelta(current, observed, requested []string) []string {
	currentTerms := translation.NormalizeProtectedTerms(current)
	observedTerms := translation.NormalizeProtectedTerms(observed)
	requestedTerms := translation.NormalizeProtectedTerms(requested)
	observedSet := make(map[string]struct{}, len(observedTerms))
	requestedSet := make(map[string]struct{}, len(requestedTerms))
	for _, term := range observedTerms {
		observedSet[term] = struct{}{}
	}
	for _, term := range requestedTerms {
		requestedSet[term] = struct{}{}
	}
	result := make([]string, 0, len(currentTerms)+len(requestedTerms))
	resultSet := make(map[string]struct{}, len(currentTerms)+len(requestedTerms))
	for _, term := range currentTerms {
		_, wasObserved := observedSet[term]
		_, stillRequested := requestedSet[term]
		if wasObserved && !stillRequested {
			continue
		}
		result = append(result, term)
		resultSet[term] = struct{}{}
	}
	for _, term := range requestedTerms {
		if _, wasObserved := observedSet[term]; wasObserved {
			continue
		}
		if _, alreadyPresent := resultSet[term]; alreadyPresent {
			continue
		}
		result = append(result, term)
		resultSet[term] = struct{}{}
	}
	return result
}
