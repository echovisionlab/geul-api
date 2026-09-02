package form

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type formDocumentObject = structured.Fields
type formDocumentArray = structured.Values

// CanonicalizeLocalizedFormSchema overlays locale-owned translated form text onto the
// current source schema so public/read paths always follow source step and field
// structure even when the locale row is stale.
func CanonicalizeLocalizedFormSchema(sourceSchema, localizedSchema []byte) ([]byte, *string, error) {
	if len(sourceSchema) == 0 {
		return localizedSchema, nil, nil
	}
	if len(localizedSchema) == 0 {
		return sourceSchema, nil, nil
	}

	localizedTexts, err := formSchemaTranslationTexts(localizedSchema)
	if err != nil {
		return nil, nil, err
	}
	var schema formDocumentObject
	if err := json.Unmarshal(sourceSchema, &schema); err != nil {
		return nil, nil, fmt.Errorf("failed to parse form content_json: %w", err)
	}
	steps := formValueSlice(schema["steps"])
	applyFormSchemaTranslationTexts(steps, localizedTexts)
	schema["steps"] = steps
	body, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode translated form schema: %w", err)
	}
	text := strings.TrimSpace(extractFormSchemaText(steps))
	if text == "" {
		return body, nil, nil
	}
	return body, &text, nil
}

// NormalizeLocalizedFormSchemaOverlay keeps only locale-owned values that are
// explicitly present in the requested target while retaining source-owned
// stable structure. Missing target units remain absent and explicit empty
// strings remain present, so read-time materialization can distinguish them.
func NormalizeLocalizedFormSchemaOverlay(sourceSchema, localizedSchema []byte) ([]byte, *string, error) {
	if len(sourceSchema) == 0 || len(localizedSchema) == 0 {
		return localizedSchema, nil, nil
	}
	localizedTexts, err := formSchemaTranslationTexts(localizedSchema)
	if err != nil {
		return nil, nil, err
	}
	var schema formDocumentObject
	if err := json.Unmarshal(sourceSchema, &schema); err != nil {
		return nil, nil, fmt.Errorf("failed to parse form content_json: %w", err)
	}
	steps := formValueSlice(schema["steps"])
	walkFormSchemaText(steps, func(object formDocumentObject, field, unitID string) {
		if translated, ok := localizedTexts[unitID]; ok {
			object[field] = preserveFormSchemaEdgeWhitespace(formStringField(object, field), translated)
			return
		}
		delete(object, field)
	})
	schema["steps"] = steps
	body, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode translated form schema: %w", err)
	}
	text := strings.TrimSpace(extractFormSchemaText(steps))
	return body, &text, nil
}

func formSchemaTranslationTexts(body []byte) (map[string]string, error) {
	var schema formDocumentObject
	if err := json.Unmarshal(body, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse form content_json: %w", err)
	}
	result := make(map[string]string)
	walkFormSchemaText(formValueSlice(schema["steps"]), func(object formDocumentObject, field, unitID string) {
		if text, ok := object[field].(string); ok {
			result[unitID] = text
		}
	})
	return result, nil
}

func applyFormSchemaTranslationTexts(steps formDocumentArray, texts map[string]string) {
	walkFormSchemaText(steps, func(object formDocumentObject, field, unitID string) {
		if translated, ok := texts[unitID]; ok {
			object[field] = preserveFormSchemaEdgeWhitespace(formStringField(object, field), translated)
		}
	})
}

func walkFormSchemaText(steps formDocumentArray, visit func(formDocumentObject, string, string)) {
	walkFormSchemaTranslationText(steps, func(text formSchemaTranslationText) {
		visit(text.object, text.field, text.unitID)
	})
}

type formSchemaTranslationText struct {
	object        formDocumentObject
	field         string
	unitID        string
	path          string
	blockHandle   string
	fieldHandle   string
	containerType string
	containerID   string
	context       *string
}

// walkFormSchemaTranslationText is the single traversal for form-owned
// translated fields. The localization projection and translation adapter both
// use it so their slot identity cannot drift apart.
func walkFormSchemaTranslationText(
	steps formDocumentArray,
	visit func(formSchemaTranslationText),
) {
	for stepIndex, rawStep := range steps {
		step, ok := rawStep.(formDocumentObject)
		if !ok {
			continue
		}
		stepID := formNodeID(step, "id", fmt.Sprintf("step:%d", stepIndex))
		visit(formSchemaTranslationText{
			object: step, field: "title", unitID: fmt.Sprintf("step:%s:title", stepID),
			path:          fmt.Sprintf("schema:step:%s:title", stepID),
			blockHandle:   intrav1.FormStepBlockHandlePrefix + stepID,
			fieldHandle:   intrav1.FormStepTitleFieldHandle,
			containerType: translation.ContainerTypeSection, containerID: stepID,
		})
		visit(formSchemaTranslationText{
			object: step, field: "description", unitID: fmt.Sprintf("step:%s:description", stepID),
			path:          fmt.Sprintf("schema:step:%s:description", stepID),
			blockHandle:   intrav1.FormStepBlockHandlePrefix + stepID,
			fieldHandle:   intrav1.FormStepDescriptionFieldHandle,
			containerType: translation.ContainerTypeSection, containerID: stepID,
		})
		for fieldIndex, rawField := range formValueSlice(step["fields"]) {
			field, ok := rawField.(formDocumentObject)
			if !ok {
				continue
			}
			fieldID := formNodeID(field, "id", fmt.Sprintf("%s:field:%d", stepID, fieldIndex))
			context := strings.TrimSpace(formStringField(field, "type"))
			var contextPointer *string
			if context != "" {
				contextPointer = &context
			}
			for _, fieldSpec := range []struct {
				name        string
				pathName    string
				fieldHandle string
			}{
				{name: "label", pathName: "label", fieldHandle: intrav1.FormFieldLabelFieldHandle},
				{name: "description", pathName: "description", fieldHandle: intrav1.FormFieldDescriptionFieldHandle},
				{name: "placeholder", pathName: "placeholder", fieldHandle: intrav1.FormFieldPlaceholderFieldHandle},
				{name: "checkboxLabel", pathName: "checkboxLabel", fieldHandle: intrav1.FormFieldCheckboxLabelFieldHandle},
			} {
				visit(formSchemaTranslationText{
					object: field, field: fieldSpec.name,
					unitID: fmt.Sprintf(
						"field:%s:%s",
						fieldID,
						strings.ReplaceAll(fieldSpec.pathName, "checkboxLabel", "checkbox_label"),
					),
					path:          fmt.Sprintf("schema:step:%s:field:%s:%s", stepID, fieldID, fieldSpec.pathName),
					blockHandle:   intrav1.FormFieldBlockHandlePrefix + fieldID,
					fieldHandle:   fieldSpec.fieldHandle,
					containerType: translation.ContainerTypeBlock, containerID: fieldID,
					context: contextPointer,
				})
			}
			for optionIndex, rawOption := range formValueSlice(field["options"]) {
				option, ok := rawOption.(formDocumentObject)
				if !ok {
					continue
				}
				optionID := formNodeID(option, "id", "")
				if optionID == "" {
					if valueKey, ok := NormalizeFormOptionValueKey(option["value"]); ok && valueKey != "" {
						optionID = valueKey
					} else {
						optionID = fmt.Sprintf("%d", optionIndex)
					}
				}
				visit(formSchemaTranslationText{
					object: option, field: "label",
					unitID:        fmt.Sprintf("field:%s:option:%s:label", fieldID, optionID),
					path:          fmt.Sprintf("schema:step:%s:field:%s:option:%s:label", stepID, fieldID, optionID),
					blockHandle:   intrav1.FormOptionBlockHandlePrefix + optionID,
					fieldHandle:   intrav1.FormOptionLabelFieldHandle,
					containerType: translation.ContainerTypeBlock, containerID: fieldID,
					context: contextPointer,
				})
			}
			validation := formMapField(field, "validation")
			for validatorIndex, rawValidator := range formValueSlice(validation["validators"]) {
				validator, ok := rawValidator.(formDocumentObject)
				if !ok {
					continue
				}
				validatorID := formNodeID(validator, "id", fmt.Sprintf("%d", validatorIndex))
				visit(formSchemaTranslationText{
					object: validator, field: "message",
					unitID:        fmt.Sprintf("field:%s:validator:%s:message", fieldID, validatorID),
					path:          fmt.Sprintf("schema:step:%s:field:%s:validator:%s:message", stepID, fieldID, validatorID),
					blockHandle:   intrav1.FormValidatorBlockHandlePrefix + validatorID,
					fieldHandle:   intrav1.FormValidatorMessageFieldHandle,
					containerType: translation.ContainerTypeBlock, containerID: fieldID,
					context: contextPointer,
				})
			}
		}
	}
}

func extractFormSchemaText(steps formDocumentArray) string {
	parts := make([]string, 0)
	walkFormSchemaText(steps, func(object formDocumentObject, field, _ string) {
		if text := strings.TrimSpace(formStringField(object, field)); text != "" {
			parts = append(parts, text)
		}
	})
	return strings.Join(parts, "\n")
}

func formValueSlice(value structured.Value) formDocumentArray {
	values, _ := value.(formDocumentArray)
	return values
}

func formMapField(value formDocumentObject, key string) formDocumentObject {
	object, _ := value[key].(formDocumentObject)
	if object == nil {
		return formDocumentObject{}
	}
	return object
}

func formStringField(value formDocumentObject, key string) string {
	raw, ok := value[key]
	if !ok || raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", raw)
}

func formNodeID(value formDocumentObject, key, fallback string) string {
	if id := strings.TrimSpace(formStringField(value, key)); id != "" {
		return id
	}
	return fallback
}

func preserveFormSchemaEdgeWhitespace(source, translated string) string {
	return translation.PreserveSourceEdgeWhitespace(source, translated)
}
