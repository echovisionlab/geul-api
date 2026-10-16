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
		return nil, &translationResponseError{event: "validation_failed", cause: fmt.Errorf("response violates XLIFF source contract")}
	}
	selected := SelectResponse(req, raw)
	return &selected, nil
}

func xliffProviderResponseFromAI(source XLIFFDocument, envelope translationJSONEnvelope) (XLIFFDocument, error) {
	expectedGroups := make(map[string]XLIFFGroup, len(source.File.Groups))
	expectedUnits := make(map[string]XLIFFUnit)
	for _, group := range source.File.Groups {
		expectedGroups[group.ID] = group
		for _, unit := range group.TranslationUnit {
			expectedUnits[unit.ID] = unit
		}
	}

	result := source
	result.File.Groups = make([]XLIFFGroup, 0, len(envelope.Bundles))
	for _, returnedGroup := range envelope.Bundles {
		group := expectedGroups[returnedGroup.BundleID]
		group.ID = returnedGroup.BundleID
		group.TranslationUnit = make([]XLIFFUnit, 0, len(returnedGroup.Units))
		for _, returnedUnit := range returnedGroup.Units {
			unit := expectedUnits[returnedUnit.UnitID]
			unit.ID = returnedUnit.UnitID
			targetInline, err := UnmarshalXLIFFInlineFragment(returnedUnit.TranslatedInline)
			if err != nil {
				return XLIFFDocument{}, err
			}
			target, err := ProjectXLIFFInline(targetInline, unit.OriginalData)
			if err != nil {
				return XLIFFDocument{}, err
			}
			unit.Target = &target
			unit.TargetInline = targetInline
			group.TranslationUnit = append(group.TranslationUnit, unit)
		}
		result.File.Groups = append(result.File.Groups, group)
	}
	return result, nil
}
