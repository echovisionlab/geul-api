package form

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// validateFormAIDocumentTargetSchema is the domain-owned fail-closed fence for
// target values. It compares the complete source-owned topology after removing
// only the inventory-defined locale copy fields at their exact schema paths.
// Explicit empty strings remain in the persisted target and are never converted
// to missing/source fallback.
func validateFormAIDocumentTargetSchema(source, target []byte) error {
	if err := validateCanonicalFormSchema(source); err != nil {
		return fmt.Errorf("form source schema is invalid: %w", err)
	}
	if err := validateCanonicalFormSchema(target); err != nil {
		return fmt.Errorf("form target schema is invalid: %w", err)
	}
	var sourceValue, targetValue any
	if err := json.Unmarshal(source, &sourceValue); err != nil {
		return errors.New("form source schema is invalid")
	}
	if err := json.Unmarshal(target, &targetValue); err != nil {
		return errors.New("form target schema is invalid")
	}
	if !reflect.DeepEqual(formAIDocumentSharedShape(sourceValue), formAIDocumentSharedShape(targetValue)) {
		return errors.New("form target schema may change locale copy only")
	}
	return nil
}

// ValidateAIDocumentAuthoringSchemas rejects legacy Form identities before
// they can become Collaboration or DCDP write handles.
func ValidateAIDocumentAuthoringSchemas(source, target []byte) error {
	if len(target) == 0 {
		return validateCanonicalFormSchema(source)
	}
	return validateFormAIDocumentTargetSchema(source, target)
}

func formAIDocumentSharedShape(value any) any {
	root, ok := cloneFormAIDocumentValue(value).(map[string]any)
	if !ok {
		return value
	}
	steps, _ := root["steps"].([]any)
	for _, rawStep := range steps {
		step, ok := rawStep.(map[string]any)
		if !ok {
			continue
		}
		delete(step, "title")
		delete(step, "description")
		fields, _ := step["fields"].([]any)
		for _, rawField := range fields {
			field, ok := rawField.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"label", "description", "placeholder", "checkboxLabel"} {
				delete(field, key)
			}
			options, _ := field["options"].([]any)
			for _, rawOption := range options {
				if option, ok := rawOption.(map[string]any); ok {
					delete(option, "label")
				}
			}
			validation, _ := field["validation"].(map[string]any)
			validators, _ := validation["validators"].([]any)
			for _, rawValidator := range validators {
				if validator, ok := rawValidator.(map[string]any); ok {
					delete(validator, "message")
				}
			}
		}
	}
	return root
}

func cloneFormAIDocumentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			result[key] = cloneFormAIDocumentValue(nested)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, nested := range typed {
			result[index] = cloneFormAIDocumentValue(nested)
		}
		return result
	default:
		return typed
	}
}
