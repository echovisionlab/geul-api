package translation

import (
	"encoding/json"
	"fmt"
)

type translationJSONEnvelope struct {
	Bundles []translationJSONBundle `json:"bundles"`
}

type translationJSONBundle struct {
	BundleID string                `json:"bundle_id"`
	Units    []translationJSONUnit `json:"units"`
}

type translationJSONUnit struct {
	UnitID           string `json:"unit_id"`
	TranslatedInline string `json:"translated_inline"`
}

// translationResponseError carries private diagnostics to the provider boundary
// while keeping the caller-facing invalid-response error and errors.Is contract.
type translationResponseError struct {
	event   string
	message string
	cause   error
}

func (err *translationResponseError) Error() string {
	if err.message != "" {
		return ErrProviderResponseInvalid.Error() + ": " + err.message
	}
	return ErrProviderResponseInvalid.Error()
}

func (*translationResponseError) Unwrap() error { return ErrProviderResponseInvalid }

func parseTranslationProviderResponse(req ProviderRequest, text string) (*ProviderResponse, error) {
	trimmed := extractJSONObject(text)
	var envelope translationJSONEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, &translationResponseError{event: "decode_failed", message: "invalid JSON envelope", cause: err}
	}

	providerDocument, err := xliffProviderResponseFromAI(req.Document, envelope)
	if err != nil {
		return nil, &translationResponseError{event: "inline_failed", cause: err}
	}
	raw := ProviderResponse{Document: providerDocument}
	validation := ValidateResponse(req.Document, raw, req.Profile.ProtectedTerms...)
	if !validation.Passed {
		return nil, &translationResponseError{event: "validation_failed", cause: fmt.Errorf(
			"response violates XLIFF source contract: missing_units=%d unknown_units=%d duplicate_units=%d line_break_units=%d placeholder_units=%d missing_placeholders=%d unexpected_placeholders=%d structural_issue=%t",
			len(validation.MissingUnitIDs), len(validation.UnknownUnitIDs), len(validation.DuplicateUnitIDs),
			len(validation.LineBreakMismatchUnitIDs), len(validation.PlaceholderMismatchUnitIDs),
			len(validation.MissingPlaceholders), len(validation.UnexpectedPlaceholders), validation.ParseError != nil,
		)}
	}
	selected := SelectResponse(req, raw)
	return &selected, nil
}

func xliffProviderResponseFromAI(source XLIFFDocument, envelope translationJSONEnvelope) (XLIFFDocument, error) {
	if len(envelope.Bundles) != len(source.File.Groups) {
		return XLIFFDocument{}, fmt.Errorf("AI response group count changed")
	}
	returnedGroups := make(map[string]translationJSONBundle, len(envelope.Bundles))
	for _, group := range envelope.Bundles {
		if _, duplicate := returnedGroups[group.BundleID]; duplicate {
			return XLIFFDocument{}, fmt.Errorf("AI response contains a duplicate group")
		}
		returnedGroups[group.BundleID] = group
	}

	result := source
	result.File.Groups = make([]XLIFFGroup, len(source.File.Groups))
	for groupIndex, sourceGroup := range source.File.Groups {
		returnedGroup, ok := returnedGroups[sourceGroup.ID]
		if !ok || len(returnedGroup.Units) != len(sourceGroup.TranslationUnit) {
			return XLIFFDocument{}, fmt.Errorf("AI response group membership changed")
		}
		returnedUnits := make(map[string]translationJSONUnit, len(returnedGroup.Units))
		for _, unit := range returnedGroup.Units {
			if _, duplicate := returnedUnits[unit.UnitID]; duplicate {
				return XLIFFDocument{}, fmt.Errorf("AI response contains a duplicate unit")
			}
			returnedUnits[unit.UnitID] = unit
		}
		group := sourceGroup
		group.TranslationUnit = make([]XLIFFUnit, len(sourceGroup.TranslationUnit))
		for unitIndex, sourceUnit := range sourceGroup.TranslationUnit {
			returnedUnit, ok := returnedUnits[sourceUnit.ID]
			if !ok {
				return XLIFFDocument{}, fmt.Errorf("AI response unit membership changed")
			}
			unit, err := normalizeXLIFFUnitInline(sourceUnit, isEmailTemplateFile(source.File.ID))
			if err != nil {
				return XLIFFDocument{}, err
			}
			targetInline, err := UnmarshalXLIFFInlineFragment(returnedUnit.TranslatedInline)
			if err != nil {
				return XLIFFDocument{}, err
			}
			targetInline, err = restoreAISourceInline(unit.SourceInline, targetInline)
			if err != nil {
				return XLIFFDocument{}, err
			}
			target, err := ProjectXLIFFInline(targetInline, unit.OriginalData)
			if err != nil {
				return XLIFFDocument{}, err
			}
			unit.Target = &target
			unit.TargetInline = targetInline
			group.TranslationUnit[unitIndex] = unit
		}
		result.File.Groups[groupIndex] = group
	}
	return result, nil
}
