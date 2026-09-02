package form

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// BuildTranslationExtractionPlan maps the Form-owned title and canonical
// schema into the provider-neutral Translation contract. content_text is a
// derived projection and is never an independent translation unit.
func BuildTranslationExtractionPlan(
	formID string,
	sourceLocale string,
	targetLocale string,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if source == nil {
		return nil, fmt.Errorf("translation source document is required")
	}

	units := make([]translation.Unit, 0, 8)
	appendEntityUnit := func(fieldName, sourceText string) {
		sourceText = strings.TrimSpace(sourceText)
		if sourceText == "" {
			return
		}
		units = append(units, translation.Unit{
			UnitID: "entity:" + fieldName, EntityType: "form", EntityID: formID,
			Path: "entity:" + fieldName, ContainerType: translation.ContainerTypeEntity,
			ContainerID: formID, FieldName: fieldName, SourceText: sourceText,
			SourceFormat: translation.SourceFormatPlainText, SourceLocale: sourceLocale,
		})
	}
	appendEntityUnit("title", source.Title)

	if len(source.ContentJSON) > 0 {
		structuredUnits, err := extractSchemaTranslationUnits(formID, sourceLocale, source.ContentJSON)
		if err != nil {
			return nil, err
		}
		units = append(units, structuredUnits...)
	}

	if len(units) == 0 {
		return nil, translation.ErrNoTranslatableUnits
	}

	contextTitle := trimmedStringPointer(source.Title)
	return &translation.ExtractionPlan{
		EntityType: "form", EntityID: formID, SourceLocale: sourceLocale,
		TargetLocale: targetLocale, ContextTitle: contextTitle,
		ProtectedTerms: translation.NormalizeProtectedTerms(source.ProtectedTerms),
		Units:          units,
		Bundles:        translation.BuildBundles("form", formID, sourceLocale, targetLocale, units, nil),
	}, nil
}

// ApplyTranslationCandidate applies Form translation results while preserving
// source-owned schema structure and raw HTML markup.
func ApplyTranslationCandidate(
	source *translation.SourceDocument,
	resultByUnit map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	if source == nil {
		return nil, fmt.Errorf("translation source document is required")
	}
	candidate := &translation.Candidate{}
	if result, ok := resultByUnit["entity:title"]; ok {
		candidate.Title = formStringPointer(strings.TrimSpace(result.TranslatedText))
	}

	if len(source.ContentJSON) > 0 {
		body, contentText, err := applySchemaTranslationCandidate(source.ContentJSON, resultByUnit)
		if err != nil {
			return nil, err
		}
		candidate.ContentJSON = body
		candidate.ContentText = contentText
	}
	return candidate, nil
}

func extractSchemaTranslationUnits(formID, sourceLocale string, body []byte) ([]translation.Unit, error) {
	var schema formDocumentObject
	if err := json.Unmarshal(body, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse form content_json: %w", err)
	}
	units := make([]translation.Unit, 0)
	walkFormSchemaTranslationText(formValueSlice(schema["steps"]), func(text formSchemaTranslationText) {
		sourceText := formStringField(text.object, text.field)
		if strings.TrimSpace(sourceText) == "" {
			return
		}
		units = append(units, translation.Unit{
			UnitID: text.unitID, EntityType: "form", EntityID: formID,
			Path: text.path, ContainerType: text.containerType, ContainerID: text.containerID,
			FieldName: "content_json", SourceText: sourceText,
			SourceFormat: translation.SourceFormatPlainText, SourceLocale: sourceLocale,
			Context: text.context,
		})
	})
	return units, nil
}

func applySchemaTranslationCandidate(
	body []byte,
	resultByUnit map[string]translation.UnitResult,
) ([]byte, *string, error) {
	var schema formDocumentObject
	if err := json.Unmarshal(body, &schema); err != nil {
		return nil, nil, fmt.Errorf("failed to parse form content_json: %w", err)
	}
	steps := formValueSlice(schema["steps"])
	walkFormSchemaTranslationText(steps, func(text formSchemaTranslationText) {
		result, ok := resultByUnit[text.unitID]
		if !ok {
			delete(text.object, text.field)
			return
		}
		text.object[text.field] = translation.PreserveSourceEdgeWhitespace(
			formStringField(text.object, text.field), result.TranslatedText,
		)
	})
	schema["steps"] = steps
	body, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode translated form schema: %w", err)
	}
	return body, formCanonicalContentText(body), nil
}

func trimmedStringPointer(value string) *string {
	return nonEmptyStringPointer(strings.TrimSpace(value))
}

func nonEmptyStringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}

func formStringPointer(value string) *string {
	copy := value
	return &copy
}
