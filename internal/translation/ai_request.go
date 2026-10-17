package translation

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
)

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
			"casing, and keep their whitespace and occurrence counts exactly as provided. If reader comprehension genuinely requires a " +
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
		"For each unit, translate sourceInline into translated_inline. sourceInline is authoritative; sourceText is context only. " +
		"Preserve every pc/ph tag and id exactly. These opaque codes represent placeholders, protected terms, or formatting. " +
		"Never replace a code with its literal spelling from sourceText or ProtectedTerms, and never add code attributes. " +
		"The server restores all native references from the source. You may reorder complete top-level pc/ph " +
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
			payloadUnit, err := translationAIUnitFromXLIFF(unit, isEmailTemplateFile(req.Document.File.ID))
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

func translationAIUnitFromXLIFF(unit XLIFFUnit, emailTemplate ...bool) (translationAIUnitPayload, error) {
	normalized, err := normalizeXLIFFUnitInline(unit, emailTemplate...)
	if err != nil {
		return translationAIUnitPayload{}, err
	}
	inline, err := MarshalXLIFFInlineFragment(aiSourceInline(normalized.SourceInline))
	if err != nil {
		return translationAIUnitPayload{}, err
	}
	return translationAIUnitPayload{
		UnitID: unit.ID, FieldName: unit.FieldName, SourceFormat: unit.SourceFormat,
		Path: unit.Name, ContainerType: unit.ContainerType, ContainerID: unit.ContainerID,
		SourceText: unit.Source, SourceInline: inline, Context: unit.Context,
	}, nil
}
