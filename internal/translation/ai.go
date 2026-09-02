package translation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/structured"
)

var translationPlaceholderPattern = regexp.MustCompile(`\{\{[^}]+\}\}`)

const translationAIProviderTimeout = 90 * time.Second

var translationResponseJSONSchema = structured.Fields{
	"type": "object",
	"properties": structured.Fields{
		"bundles": structured.Fields{
			"type": "array",
			"items": structured.Fields{
				"type": "object",
				"properties": structured.Fields{
					"bundle_id": structured.Fields{
						"type": "string",
					},
					"units": structured.Fields{
						"type": "array",
						"items": structured.Fields{
							"type": "object",
							"properties": structured.Fields{
								"unit_id": structured.Fields{
									"type": "string",
								},
								"translated_inline": structured.Fields{
									"type": "string",
								},
							},
							"required":             []string{"unit_id", "translated_inline"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"bundle_id", "units"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"bundles"},
	"additionalProperties": false,
}

type translationAIRequestPayload struct {
	RequestID   string                       `json:"requestId"`
	OperationID string                       `json:"operationId"`
	Profile     GenerationProfile            `json:"profile"`
	Bundles     []translationAIBundlePayload `json:"bundles"`
}

type translationAIBundlePayload struct {
	BundleID      string                     `json:"bundleId"`
	ContextTitle  *string                    `json:"contextTitle,omitempty"`
	ContextBefore *string                    `json:"contextBefore,omitempty"`
	ContextAfter  *string                    `json:"contextAfter,omitempty"`
	Units         []translationAIUnitPayload `json:"units"`
}

type translationAIUnitPayload struct {
	UnitID        string  `json:"unitId"`
	FieldName     string  `json:"fieldName,omitempty"`
	SourceFormat  string  `json:"sourceFormat,omitempty"`
	Path          string  `json:"path,omitempty"`
	ContainerType string  `json:"containerType,omitempty"`
	ContainerID   string  `json:"containerId,omitempty"`
	SourceText    string  `json:"sourceText"`
	SourceInline  string  `json:"sourceInline"`
	Context       *string `json:"context,omitempty"`
}

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

type aiTranslationGenerator struct {
	provider llm.Provider
}

type aiTranslationGeneratorSession struct {
	session llm.Session
}

func NewAIGenerator(provider llm.Provider) Generator {
	dependencycheck.New("AITranslationGenerator").
		RequireNotNil(provider, "provider").
		Validate()

	return &aiTranslationGenerator{provider: provider}
}

func (g *aiTranslationGenerator) ProviderName() string {
	return g.provider.ProviderName()
}

func (g *aiTranslationGenerator) ModelName() string {
	return g.provider.ModelName()
}

func (g *aiTranslationGenerator) Translate(
	ctx context.Context,
	req ProviderRequest,
) (*ProviderResponse, error) {
	session, err := g.StartSession(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = session.Close(ctx)
	}()

	return session.Translate(ctx, req)
}

func (g *aiTranslationGenerator) StartSession(
	ctx context.Context,
	req ProviderRequest,
) (GeneratorSession, error) {
	if err := ValidateProviderRequest(req); err != nil {
		return nil, err
	}

	session, err := g.provider.StartSession(ctx, llm.SessionSpec{
		RequestID:          req.RequestID,
		Action:             "translation-json",
		SystemPrompt:       translationAISystemPrompt(req),
		ResponseJSONSchema: translationResponseJSONSchema,
		ResponseSchemaName: "geul_translation_response",
		Timeout:            translationAIProviderTimeout,
	})
	if err != nil {
		return nil, err
	}

	return &aiTranslationGeneratorSession{session: session}, nil
}

func (s *aiTranslationGeneratorSession) Translate(
	ctx context.Context,
	req ProviderRequest,
) (*ProviderResponse, error) {
	userPrompt, err := buildTranslationAIUserPrompt(req)
	if err != nil {
		return nil, err
	}

	text, err := s.session.GenerateText(ctx, llm.SessionTurn{
		OperationID: req.OperationID,
		TurnKind:    "initial",
		UserPrompt:  userPrompt,
	})
	if err != nil {
		return nil, err
	}
	raw, err := NewProviderRawResponse("application/json", []byte(text))
	if err != nil {
		return nil, err
	}
	response, parseErr := parseTranslationProviderResponse(req, text)
	if response == nil {
		response = &ProviderResponse{}
	}
	response.Raw = raw
	return response, parseErr
}

func (s *aiTranslationGeneratorSession) Close(ctx context.Context) error {
	return s.session.Close(ctx)
}

func translationAISystemPrompt(req ProviderRequest) string {
	profile := req.Profile
	markupInstruction := "The source text is plain text."
	if profile.PreserveMarkup {
		markupInstruction = "Some units are extracted HTML text nodes. Translate only the visible text for that node. " +
			"Do not introduce HTML tags, do not remove placeholders, and keep placeholder tokens exactly unchanged."
	}
	protectedTermsInstruction := ""
	if len(profile.ProtectedTerms) > 0 {
		protectedTermsInstruction = "ProtectedTerms lists names or canonical labels that must preserve their original " +
			"source form exactly. Do not replace them with translated or transliterated forms, do not normalize their " +
			"casing, and keep their whitespace exactly as provided. If reader comprehension genuinely requires a " +
			"localized rendering, keep the original source form first and add the localized form only once in " +
			"parentheses immediately after it."
	}
	styleInstruction := translationStylePrompt(profile)
	personNamesInstruction := "When source text contains a person, artist, label, release, or track name or title, " +
		"keep the original source-language form in the translation. Only if it is truly necessary for reader " +
		"comprehension may " +
		"you append a localized rendering in parentheses immediately after the original form, like " +
		"`Original Name (Localized Name)`. Do not drop the original form, do not invert the order, and do not add " +
		"parenthetical localized names unless they are genuinely needed."
	promptFormat := "You translate publishing-system content from %s to %s. " +
		"Return only valid JSON matching the response schema. Preserve placeholders exactly, preserve unit ordering, " +
		"preserve opaque identifiers, and do not add commentary. Never invent, normalize, or edit any unitId. " +
		"Copy every unitId exactly as provided in the request and return exactly one output unit for each input unit. " +
		"Preserve literal newline characters within each unit: if a source unit contains line breaks, the " +
		"translated_inline for that same unit must preserve the same line-break structure instead of flattening it " +
		"into spaces. Translate only " +
		"explanatory prose. Do not translate or transliterate people names, artist names, label names, release titles, " +
		"track titles, handles, URLs, catalog numbers, or other canonical entity names unless the source already " +
		"includes a localized form. %s %s %s %s %s Field names indicate whether a unit is a title, summary, " +
		"or body slot. Source format indicates whether the unit came from plain text or an HTML text node. " +
		"For each unit, translate sourceInline into translated_inline. Preserve every pc/ph tag, id, dataRef, " +
		"dataRefStart, dataRefEnd, canCopy, and canDelete value exactly. You may reorder complete top-level pc/ph " +
		"tokens when target-language grammar requires it, but must not move a child outside its original parent pc. " +
		"Do not put translated text outside the styled pc runs when sourceInline uses them. Path, containerType, and " +
		"containerId identify where the unit will be applied back into the structured document."

	return fmt.Sprintf(
		promptFormat,
		profile.SourceLocale,
		profile.TargetLocale,
		"Translate every unit from source to target locale.",
		markupInstruction,
		protectedTermsInstruction,
		styleInstruction,
		personNamesInstruction,
	)
}

func translationStylePrompt(profile GenerationProfile) string {
	parts := make([]string, 0, len(profile.StyleInstructions)+3)
	if profile.ContentKind != "" {
		parts = append(parts, fmt.Sprintf("Content kind: %s.", profile.ContentKind))
	}
	if profile.TargetRegister != "" {
		parts = append(parts, fmt.Sprintf("Target register: %s.", profile.TargetRegister))
	}
	if profile.RegisterPolicy != "" {
		parts = append(parts, fmt.Sprintf("Register policy: %s.", profile.RegisterPolicy))
	}
	for _, instruction := range profile.StyleInstructions {
		trimmed := strings.TrimSpace(instruction)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Style policy: " + strings.Join(parts, " ")
}

func buildTranslationAIUserPrompt(req ProviderRequest) (string, error) {
	payload := translationAIRequestPayload{
		RequestID:   req.RequestID,
		OperationID: req.OperationID,
		Profile:     req.Profile,
		Bundles:     make([]translationAIBundlePayload, 0, len(req.Document.File.Groups)),
	}
	for _, bundle := range req.Document.File.Groups {
		nextBundle := translationAIBundlePayload{
			BundleID:      bundle.ID,
			ContextTitle:  bundle.ContextTitle,
			ContextBefore: bundle.ContextBefore,
			ContextAfter:  bundle.ContextAfter,
			Units:         make([]translationAIUnitPayload, 0, len(bundle.TranslationUnit)),
		}
		for _, unit := range bundle.TranslationUnit {
			payloadUnit, err := translationAIUnitFromXLIFF(unit)
			if err != nil {
				return "", err
			}
			nextBundle.Units = append(nextBundle.Units, payloadUnit)
		}
		payload.Bundles = append(payload.Bundles, nextBundle)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to encode translation request payload: %w", err)
	}
	return string(body), nil
}

func parseTranslationProviderResponse(req ProviderRequest, text string) (*ProviderResponse, error) {
	trimmed := extractJSONObject(text)
	var envelope translationJSONEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON envelope", ErrProviderResponseInvalid)
	}

	providerDocument, err := xliffProviderResponseFromAI(req.Document, envelope)
	if err != nil {
		return nil, ErrProviderResponseInvalid
	}
	raw := ProviderResponse{Document: providerDocument}
	validation := ValidateResponse(req.Document, raw)
	if !validation.Passed {
		return nil, ErrProviderResponseInvalid
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

func ValidateResponse(
	expected XLIFFDocument,
	actual ProviderResponse,
) HardValidationResult {
	expectedUnits := map[string]struct{}{}
	expectedUnitTexts := map[string]string{}
	expectedPlaceholders := map[string]int{}
	for _, bundle := range expected.File.Groups {
		for _, unit := range bundle.TranslationUnit {
			expectedUnits[unit.ID] = struct{}{}
			expectedUnitTexts[unit.ID] = unit.Source
			for _, placeholder := range ExtractPlaceholders(unit.Source) {
				expectedPlaceholders[placeholder]++
			}
		}
	}

	seenUnits := map[string]int{}
	actualPlaceholders := map[string]int{}
	var missingUnits []string
	var lineBreakMismatchUnits []string
	var placeholderMismatchUnitIDs []string
	var unknownUnits []string
	var duplicateUnits []string
	parseIssues := validateXLIFFResponseIdentity(expected, actual.Document)

	for _, bundle := range actual.Document.File.Groups {
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
				if err := validateXLIFFUnitInline(unit, true); err != nil {
					parseIssues = append(parseIssues, fmt.Sprintf("XLIFF unit %q inline structure changed", unit.ID))
				}
				if CountLineBreaks(expectedUnitTexts[unit.ID]) != CountLineBreaks(*unit.Target) {
					lineBreakMismatchUnits = append(lineBreakMismatchUnits, unit.ID)
				}
				if !SamePlaceholders(expectedUnitTexts[unit.ID], *unit.Target) {
					placeholderMismatchUnitIDs = append(placeholderMismatchUnitIDs, unit.ID)
				}
			}
			for _, placeholder := range ExtractPlaceholders(*unit.Target) {
				actualPlaceholders[placeholder]++
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
		for range expectedCount - actualPlaceholders[placeholder] {
			missingPlaceholders = append(missingPlaceholders, placeholder)
		}
	}

	unexpectedPlaceholders := make([]string, 0)
	for placeholder, actualCount := range actualPlaceholders {
		for range actualCount - expectedPlaceholders[placeholder] {
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

func validateXLIFFResponseIdentity(expected XLIFFDocument, actual XLIFFDocument) []string {
	issues := make([]string, 0)
	if actual.Version != expected.Version {
		issues = append(issues, "XLIFF version changed")
	}
	if actual.SourceLocale != expected.SourceLocale {
		issues = append(issues, "XLIFF source locale changed")
	}
	if actual.TargetLocale != expected.TargetLocale {
		issues = append(issues, "XLIFF target locale changed")
	}
	if actual.File.ID != expected.File.ID {
		issues = append(issues, "XLIFF file id changed")
	}
	if len(actual.File.Groups) != len(expected.File.Groups) {
		issues = append(issues, "XLIFF group count changed")
	}

	expectedSources := make(map[string]string)
	for groupIndex, group := range expected.File.Groups {
		if groupIndex >= len(actual.File.Groups) || actual.File.Groups[groupIndex].ID != group.ID {
			issues = append(issues, fmt.Sprintf("XLIFF group %q changed", group.ID))
		} else {
			actualGroup := actual.File.Groups[groupIndex]
			if actualGroup.SequenceIndex != group.SequenceIndex || actualGroup.SequenceTotal != group.SequenceTotal ||
				pointerValue(actualGroup.ContextTitle) != pointerValue(group.ContextTitle) ||
				pointerValue(actualGroup.ContextBefore) != pointerValue(group.ContextBefore) ||
				pointerValue(actualGroup.ContextAfter) != pointerValue(group.ContextAfter) {
				issues = append(issues, fmt.Sprintf("XLIFF group %q metadata changed", group.ID))
			}
			if len(actualGroup.TranslationUnit) == len(group.TranslationUnit) {
				for unitIndex, unit := range group.TranslationUnit {
					if actualGroup.TranslationUnit[unitIndex].ID != unit.ID {
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
	for _, group := range actual.File.Groups {
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

func translationAIUnitFromXLIFF(unit XLIFFUnit) (translationAIUnitPayload, error) {
	normalized, err := normalizeXLIFFUnitInline(unit)
	if err != nil {
		return translationAIUnitPayload{}, err
	}
	inline, err := MarshalXLIFFInlineFragment(normalized.SourceInline)
	if err != nil {
		return translationAIUnitPayload{}, err
	}
	return translationAIUnitPayload{
		UnitID: unit.ID, FieldName: unit.FieldName, SourceFormat: unit.SourceFormat,
		Path: unit.Name, ContainerType: unit.ContainerType, ContainerID: unit.ContainerID,
		SourceText: unit.Source, SourceInline: inline, Context: unit.Context,
	}, nil
}

func SamePlaceholders(sourceText string, translatedText string) bool {
	source := ExtractPlaceholders(sourceText)
	translated := ExtractPlaceholders(translatedText)
	if len(source) != len(translated) {
		return false
	}
	expected := make(map[string]int, len(source))
	for _, placeholder := range source {
		expected[placeholder]++
	}
	for _, placeholder := range translated {
		if expected[placeholder] == 0 {
			return false
		}
		expected[placeholder]--
	}
	return true
}

func ExtractPlaceholders(value string) []string {
	return translationPlaceholderPattern.FindAllString(value, -1)
}

func CountLineBreaks(value string) int {
	return strings.Count(value, "\n")
}
