package translation

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateResponse checks translated content against the source contract without
// provider I/O or logging. Callers own reporting failures in their request context.
func ValidateResponse(
	expected XLIFFDocument,
	response ProviderResponse,
	protectedTerms ...string,
) HardValidationResult {
	expectedUnits := map[string]struct{}{}
	expectedUnitTexts := map[string]string{}
	expectedPlaceholders := map[string]int{}
	for _, bundle := range expected.File.Groups {
		for _, unit := range bundle.TranslationUnit {
			expectedUnits[unit.ID] = struct{}{}
			expectedUnitTexts[unit.ID] = unit.Source
			for _, placeholder := range documentPlaceholders(expected, unit.Source) {
				expectedPlaceholders[placeholder]++
			}
		}
	}

	seenUnits := map[string]int{}
	translatedPlaceholders := map[string]int{}
	var missingUnits []string
	var lineBreakMismatchUnits []string
	var placeholderMismatchUnitIDs []string
	var unknownUnits []string
	var duplicateUnits []string
	parseIssues := validateXLIFFResponseIdentity(expected, response.Document)

	for _, bundle := range response.Document.File.Groups {
		for _, unit := range bundle.TranslationUnit {
			if unit.Target == nil {
				continue
			}
			seenUnits[unit.ID]++
			if seenUnits[unit.ID] == 2 {
				duplicateUnits = append(duplicateUnits, unit.ID)
			}
			if _, ok := expectedUnits[unit.ID]; !ok {
				unknownUnits = append(unknownUnits, unit.ID)
			} else {
				if strings.TrimSpace(*unit.Target) == "" && strings.TrimSpace(expectedUnitTexts[unit.ID]) != "" {
					parseIssues = append(parseIssues, fmt.Sprintf("XLIFF unit %q target is empty", unit.ID))
				}
				if err := validateXLIFFUnitInline(unit, true, isEmailTemplateFile(expected.File.ID)); err != nil {
					parseIssues = append(parseIssues, fmt.Sprintf("XLIFF unit %q inline structure changed", unit.ID))
				}
				if CountLineBreaks(expectedUnitTexts[unit.ID]) != CountLineBreaks(*unit.Target) {
					lineBreakMismatchUnits = append(lineBreakMismatchUnits, unit.ID)
				}
				for _, term := range protectedTerms {
					if term != "" && strings.Count(expectedUnitTexts[unit.ID], term) != strings.Count(*unit.Target, term) {
						parseIssues = append(parseIssues, fmt.Sprintf("XLIFF unit %q protected term count changed", unit.ID))
						break
					}
				}
				if !sameDocumentPlaceholders(expected, expectedUnitTexts[unit.ID], *unit.Target) {
					placeholderMismatchUnitIDs = append(placeholderMismatchUnitIDs, unit.ID)
				}
			}
			for _, placeholder := range documentPlaceholders(expected, *unit.Target) {
				translatedPlaceholders[placeholder]++
			}
		}
	}

	for unitID := range expectedUnits {
		if seenUnits[unitID] == 0 {
			missingUnits = append(missingUnits, unitID)
		}
	}

	missingPlaceholders := make([]string, 0)
	for placeholder, expectedCount := range expectedPlaceholders {
		for range expectedCount - translatedPlaceholders[placeholder] {
			missingPlaceholders = append(missingPlaceholders, placeholder)
		}
	}

	unexpectedPlaceholders := make([]string, 0)
	for placeholder, translatedCount := range translatedPlaceholders {
		for range translatedCount - expectedPlaceholders[placeholder] {
			unexpectedPlaceholders = append(unexpectedPlaceholders, placeholder)
		}
	}
	sort.Strings(missingUnits)
	sort.Strings(lineBreakMismatchUnits)
	sort.Strings(placeholderMismatchUnitIDs)
	sort.Strings(unknownUnits)
	sort.Strings(duplicateUnits)
	sort.Strings(missingPlaceholders)
	sort.Strings(unexpectedPlaceholders)

	passed := len(missingUnits) == 0 && len(lineBreakMismatchUnits) == 0 &&
		len(placeholderMismatchUnitIDs) == 0 && len(unknownUnits) == 0 && len(duplicateUnits) == 0 &&
		len(missingPlaceholders) == 0 && len(unexpectedPlaceholders) == 0 && len(parseIssues) == 0
	var parseError *string
	if len(parseIssues) > 0 {
		message := strings.Join(parseIssues, "; ")
		parseError = &message
	}
	return HardValidationResult{
		Passed:                     passed,
		MissingUnitIDs:             missingUnits,
		LineBreakMismatchUnitIDs:   lineBreakMismatchUnits,
		PlaceholderMismatchUnitIDs: placeholderMismatchUnitIDs,
		UnknownUnitIDs:             unknownUnits,
		DuplicateUnitIDs:           duplicateUnits,
		MissingPlaceholders:        missingPlaceholders,
		UnexpectedPlaceholders:     unexpectedPlaceholders,
		ParseError:                 parseError,
	}
}

func validateXLIFFResponseIdentity(expected XLIFFDocument, response XLIFFDocument) []string {
	issues := make([]string, 0)
	if response.Version != expected.Version {
		issues = append(issues, "XLIFF version changed")
	}
	if response.SourceLocale != expected.SourceLocale {
		issues = append(issues, "XLIFF source locale changed")
	}
	if response.TargetLocale != expected.TargetLocale {
		issues = append(issues, "XLIFF target locale changed")
	}
	if response.File.ID != expected.File.ID {
		issues = append(issues, "XLIFF file id changed")
	}
	if len(response.File.Groups) != len(expected.File.Groups) {
		issues = append(issues, "XLIFF group count changed")
	}

	expectedSources := make(map[string]string)
	for groupIndex, group := range expected.File.Groups {
		if groupIndex >= len(response.File.Groups) || response.File.Groups[groupIndex].ID != group.ID {
			issues = append(issues, fmt.Sprintf("XLIFF group %q changed", group.ID))
		} else {
			responseGroup := response.File.Groups[groupIndex]
			if responseGroup.SequenceIndex != group.SequenceIndex || responseGroup.SequenceTotal != group.SequenceTotal ||
				pointerValue(responseGroup.ContextTitle) != pointerValue(group.ContextTitle) ||
				pointerValue(responseGroup.ContextBefore) != pointerValue(group.ContextBefore) ||
				pointerValue(responseGroup.ContextAfter) != pointerValue(group.ContextAfter) {
				issues = append(issues, fmt.Sprintf("XLIFF group %q metadata changed", group.ID))
			}
			if len(responseGroup.TranslationUnit) == len(group.TranslationUnit) {
				for unitIndex, unit := range group.TranslationUnit {
					if responseGroup.TranslationUnit[unitIndex].ID != unit.ID {
						issues = append(issues, fmt.Sprintf("XLIFF group %q unit order changed", group.ID))
						break
					}
				}
			}
		}
		for _, unit := range group.TranslationUnit {
			expectedSources[unit.ID] = unit.Source
		}
	}
	for _, group := range response.File.Groups {
		for _, unit := range group.TranslationUnit {
			if source, known := expectedSources[unit.ID]; known && unit.Source != source {
				issues = append(issues, fmt.Sprintf("XLIFF unit %q source changed", unit.ID))
			}
		}
	}
	return issues
}

func FlattenResponse(result ProviderResponse) map[string]UnitResult {
	return XLIFFTargets(result.Document)
}
