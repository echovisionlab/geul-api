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
// use it so their slot identity cannot drift apart. Each level below a step
// is its own walk: a field, its options and its validators.
func walkFormSchemaTranslationText(
	steps formDocumentArray,
	visit func(formSchemaTranslationText),
) {
	for stepIndex, rawStep := range steps {
		step, ok := rawStep.(formDocumentObject)
		if !ok {
			continue
		}
		stepID := formNodeID(step, fmt.Sprintf("step:%d", stepIndex))
		for _, stepSpec := range []struct{ name, fieldHandle string }{
			{name: "title", fieldHandle: intrav1.FormStepTitleFieldHandle},
			{name: "description", fieldHandle: intrav1.FormStepDescriptionFieldHandle},
		} {
			visit(formSchemaTranslationText{
				object: step, field: stepSpec.name, unitID: fmt.Sprintf("step:%s:%s", stepID, stepSpec.name),
				path:          fmt.Sprintf("schema:step:%s:%s", stepID, stepSpec.name),
				blockHandle:   intrav1.FormStepBlockHandlePrefix + stepID,
				fieldHandle:   stepSpec.fieldHandle,
				containerType: translation.ContainerTypeSection, containerID: stepID,
			})
		}
		for fieldIndex, rawField := range formValueSlice(step["fields"]) {
			if field, ok := rawField.(formDocumentObject); ok {
				walkFormFieldTranslationText(stepID, formNodeID(field, fmt.Sprintf("%s:field:%d", stepID, fieldIndex)), field, visit)
			}
		}
	}
}

// walkFormFieldTranslationText visits one field's translated copy, then its
// options' and validators' copy. Every slot of the field carries the field
// type as its translation context.
func walkFormFieldTranslationText(stepID, fieldID string, field formDocumentObject, visit func(formSchemaTranslationText)) {
	var context *string
	if fieldType := strings.TrimSpace(formStringField(field, "type")); fieldType != "" {
		context = &fieldType
	}
	for _, fieldSpec := range []struct {
		name        string
		unitName    string
		fieldHandle string
	}{
		{name: "label", unitName: "label", fieldHandle: intrav1.FormFieldLabelFieldHandle},
		{name: "description", unitName: "description", fieldHandle: intrav1.FormFieldDescriptionFieldHandle},
		{name: "placeholder", unitName: "placeholder", fieldHandle: intrav1.FormFieldPlaceholderFieldHandle},
		{name: "checkboxLabel", unitName: "checkbox_label", fieldHandle: intrav1.FormFieldCheckboxLabelFieldHandle},
	} {
		visit(formSchemaTranslationText{
			object: field, field: fieldSpec.name,
			unitID:        fmt.Sprintf("field:%s:%s", fieldID, fieldSpec.unitName),
			path:          fmt.Sprintf("schema:step:%s:field:%s:%s", stepID, fieldID, fieldSpec.name),
			blockHandle:   intrav1.FormFieldBlockHandlePrefix + fieldID,
			fieldHandle:   fieldSpec.fieldHandle,
			containerType: translation.ContainerTypeBlock, containerID: fieldID,
			context: context,
		})
	}
	for optionIndex, rawOption := range formValueSlice(field["options"]) {
		option, ok := rawOption.(formDocumentObject)
		if !ok {
			continue
		}
		optionID := formOptionTranslationID(option, optionIndex)
		visit(formSchemaTranslationText{
			object: option, field: "label",
			unitID:        fmt.Sprintf("field:%s:option:%s:label", fieldID, optionID),
			path:          fmt.Sprintf("schema:step:%s:field:%s:option:%s:label", stepID, fieldID, optionID),
			blockHandle:   intrav1.FormOptionBlockHandlePrefix + optionID,
			fieldHandle:   intrav1.FormOptionLabelFieldHandle,
			containerType: translation.ContainerTypeBlock, containerID: fieldID,
			context: context,
		})
	}
	for validatorIndex, rawValidator := range formValueSlice(formMapField(field, "validation")["validators"]) {
		validator, ok := rawValidator.(formDocumentObject)
		if !ok {
			continue
		}
		validatorID := formNodeID(validator, fmt.Sprintf("%d", validatorIndex))
		visit(formSchemaTranslationText{
			object: validator, field: "message",
			unitID:        fmt.Sprintf("field:%s:validator:%s:message", fieldID, validatorID),
			path:          fmt.Sprintf("schema:step:%s:field:%s:validator:%s:message", stepID, fieldID, validatorID),
			blockHandle:   intrav1.FormValidatorBlockHandlePrefix + validatorID,
			fieldHandle:   intrav1.FormValidatorMessageFieldHandle,
			containerType: translation.ContainerTypeBlock, containerID: fieldID,
			context: context,
		})
	}
}

// formOptionTranslationID is an option's stable ID, else its normalized
// value, else its position.
func formOptionTranslationID(option formDocumentObject, index int) string {
	if id := formNodeID(option, ""); id != "" {
		return id
	}
	if valueKey, ok := NormalizeFormOptionValueKey(option["value"]); ok && valueKey != "" {
		return valueKey
	}
	return fmt.Sprintf("%d", index)
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

func formNodeID(value formDocumentObject, fallback string) string {
	if id := strings.TrimSpace(formStringField(value, "id")); id != "" {
		return id
	}
	return fallback
}

func preserveFormSchemaEdgeWhitespace(source, translated string) string {
	return translation.PreserveSourceEdgeWhitespace(source, translated)
}
