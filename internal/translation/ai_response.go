package translation

import (
	"encoding/json"
	"errors"
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
// issues holds bounded issue kinds for logs and spans.
type translationResponseError struct {
	event   string
	message string
	issues  []string
	cause   error
}

func (err *translationResponseError) Error() string {
	if err.message != "" {
		return ErrProviderResponseInvalid.Error() + ": " + err.message
	}
	return ErrProviderResponseInvalid.Error()
}

func (*translationResponseError) Unwrap() error { return ErrProviderResponseInvalid }

// aiResponseShapeError names the first membership or inline failure while
// mapping the AI envelope onto the source document. Its message carries only
// source-owned group and unit IDs and fixed validator text.
type aiResponseShapeError struct {
	kind string
	err  error
}

func (err *aiResponseShapeError) Error() string { return err.err.Error() }

func (err *aiResponseShapeError) Unwrap() error { return err.err }

func aiResponseShapeFailure(kind string, format string, args ...any) error {
	return &aiResponseShapeError{kind: kind, err: fmt.Errorf(format, args...)}
}

func parseTranslationProviderResponse(req ProviderRequest, text string) (*ProviderResponse, error) {
	trimmed := extractJSONObject(text)
	var envelope translationJSONEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, &translationResponseError{
			event: "decode_failed", message: "invalid JSON envelope",
			issues: []string{responseIssueEnvelopeInvalid}, cause: err,
		}
	}

	providerDocument, err := xliffProviderResponseFromAI(req.Document, envelope)
	if err != nil {
		kind := responseIssueInlineInvalid
		var shapeErr *aiResponseShapeError
		if errors.As(err, &shapeErr) {
			kind = shapeErr.kind
		}
		return nil, &translationResponseError{event: "inline_failed", issues: []string{kind}, cause: err}
	}
	raw := ProviderResponse{Document: providerDocument}
	validation := ValidateResponse(req.Document, raw, req.Profile.ProtectedTerms...)
	if !validation.Passed {
		return nil, ResponseValidationError(validation)
	}
	selected := SelectResponse(req, raw)
	return &selected, nil
}

func xliffProviderResponseFromAI(source XLIFFDocument, envelope translationJSONEnvelope) (XLIFFDocument, error) {
	if len(envelope.Bundles) != len(source.File.Groups) {
		return XLIFFDocument{}, aiResponseShapeFailure(responseIssueGroupCountChanged,
			"AI response group count changed: expected=%d returned=%d", len(source.File.Groups), len(envelope.Bundles))
	}
	sourceGroups := make(map[string]struct{}, len(source.File.Groups))
	for _, group := range source.File.Groups {
		sourceGroups[group.ID] = struct{}{}
	}
	returnedGroups := make(map[string]translationJSONBundle, len(envelope.Bundles))
	for _, group := range envelope.Bundles {
		if _, duplicate := returnedGroups[group.BundleID]; duplicate {
			if _, known := sourceGroups[group.BundleID]; known {
				return XLIFFDocument{}, aiResponseShapeFailure(responseIssueDuplicateGroup,
					"AI response contains a duplicate group: group=%q", group.BundleID)
			}
			return XLIFFDocument{}, aiResponseShapeFailure(responseIssueDuplicateGroup, "AI response contains a duplicate group")
		}
		returnedGroups[group.BundleID] = group
	}

	result := source
	result.File.Groups = make([]XLIFFGroup, len(source.File.Groups))
	for groupIndex, sourceGroup := range source.File.Groups {
		returnedGroup, ok := returnedGroups[sourceGroup.ID]
		if !ok {
			return XLIFFDocument{}, aiResponseShapeFailure(responseIssueGroupMembershipChanged,
				"AI response group membership changed: group=%q missing", sourceGroup.ID)
		}
		if len(returnedGroup.Units) != len(sourceGroup.TranslationUnit) {
			return XLIFFDocument{}, aiResponseShapeFailure(responseIssueGroupMembershipChanged,
				"AI response group membership changed: group=%q expected_units=%d returned_units=%d",
				sourceGroup.ID, len(sourceGroup.TranslationUnit), len(returnedGroup.Units))
		}
		sourceUnits := make(map[string]struct{}, len(sourceGroup.TranslationUnit))
		for _, unit := range sourceGroup.TranslationUnit {
			sourceUnits[unit.ID] = struct{}{}
		}
		returnedUnits := make(map[string]translationJSONUnit, len(returnedGroup.Units))
		for _, unit := range returnedGroup.Units {
			if _, duplicate := returnedUnits[unit.UnitID]; duplicate {
				if _, known := sourceUnits[unit.UnitID]; known {
					return XLIFFDocument{}, aiResponseShapeFailure(responseIssueDuplicateUnit,
						"AI response contains a duplicate unit: group=%q unit=%q", sourceGroup.ID, unit.UnitID)
				}
				return XLIFFDocument{}, aiResponseShapeFailure(responseIssueDuplicateUnit,
					"AI response contains a duplicate unit: group=%q", sourceGroup.ID)
			}
			returnedUnits[unit.UnitID] = unit
		}
		group := sourceGroup
		group.TranslationUnit = make([]XLIFFUnit, len(sourceGroup.TranslationUnit))
		for unitIndex, sourceUnit := range sourceGroup.TranslationUnit {
			returnedUnit, ok := returnedUnits[sourceUnit.ID]
			if !ok {
				return XLIFFDocument{}, aiResponseShapeFailure(responseIssueMissingUnit,
					"AI response unit membership changed: group=%q unit=%q missing", sourceGroup.ID, sourceUnit.ID)
			}
			unit, err := aiResponseUnit(source, sourceUnit, returnedUnit)
			if err != nil {
				return XLIFFDocument{}, aiResponseShapeFailure(responseIssueInlineInvalid,
					"AI response unit inline invalid: group=%q unit=%q: %w", sourceGroup.ID, sourceUnit.ID, err)
			}
			group.TranslationUnit[unitIndex] = unit
		}
		result.File.Groups[groupIndex] = group
	}
	return result, nil
}

func aiResponseUnit(source XLIFFDocument, sourceUnit XLIFFUnit, returnedUnit translationJSONUnit) (XLIFFUnit, error) {
	unit, err := normalizeXLIFFUnitInline(sourceUnit, isEmailTemplateFile(source.File.ID))
	if err != nil {
		return XLIFFUnit{}, err
	}
	targetInline, err := UnmarshalXLIFFInlineFragment(returnedUnit.TranslatedInline)
	if err != nil {
		return XLIFFUnit{}, err
	}
	targetInline, err = restoreAISourceInline(unit.SourceInline, targetInline)
	if err != nil {
		return XLIFFUnit{}, err
	}
	target, err := ProjectXLIFFInline(targetInline, unit.OriginalData)
	if err != nil {
		return XLIFFUnit{}, err
	}
	unit.Target = &target
	unit.TargetInline = targetInline
	return unit, nil
}
