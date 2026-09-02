package form

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

const formReservedSubmissionMetadataPrefix = "__meta."

var allowedFormFieldTypes = map[string]struct{}{
	"text":        {},
	"email":       {},
	"textarea":    {},
	"tel":         {},
	"number":      {},
	"select":      {},
	"multiselect": {},
	"checkbox":    {},
	"switch":      {},
	"date":        {},
}

type formSchemaPayload struct {
	ID    string                  `json:"id"`
	Steps []formSchemaStepPayload `json:"steps"`
}

type formSchemaStepPayload struct {
	ID        string                   `json:"id"`
	Title     string                   `json:"title"`
	Fields    []formSchemaFieldPayload `json:"fields,omitempty"`
	Condition json.RawMessage          `json:"condition,omitempty"`
}

type formSchemaFieldPayload struct {
	ID         string                     `json:"id"`
	Key        string                     `json:"key"`
	Name       string                     `json:"name"`
	Label      string                     `json:"label"`
	Type       string                     `json:"type"`
	Options    []formSchemaOptionPayload  `json:"options,omitempty"`
	Condition  json.RawMessage            `json:"condition,omitempty"`
	Validation formFieldValidationPayload `json:"validation"`
}

type formSchemaOptionPayload struct {
	ID    string           `json:"id"`
	Value structured.Value `json:"value"`
	Label string           `json:"label"`
}

type formSchemaConditionPayload struct {
	FieldID string `json:"fieldId"`
	Field   string `json:"field"`
}

type formSchemaConditionGroupPayload struct {
	Conditions []json.RawMessage `json:"conditions"`
}

type formSchemaMetadata struct {
	OrderedFieldKeys []string
	FieldLabels      map[string]string
}

func resolveFormFieldKey(field formSchemaFieldPayload) string {
	fieldKey := strings.TrimSpace(field.Key)
	if fieldKey != "" {
		return fieldKey
	}
	return strings.TrimSpace(field.Name)
}

func resolveFormFieldLabel(field formSchemaFieldPayload) string {
	fieldLabel := strings.TrimSpace(field.Label)
	if fieldLabel != "" {
		return fieldLabel
	}

	fieldKey := resolveFormFieldKey(field)
	if fieldKey != "" {
		return fieldKey
	}

	return strings.TrimSpace(field.ID)
}

func resolveFormConditionFieldID(condition formSchemaConditionPayload) string {
	fieldID := strings.TrimSpace(condition.FieldID)
	if fieldID != "" {
		return fieldID
	}
	return strings.TrimSpace(condition.Field)
}

func NormalizeFormOptionValueKey(value structured.Value) (string, bool) {
	switch v := value.(type) {
	case string:
		return "string:" + v, true
	case float64:
		return "number:" + strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return "bool:" + strconv.FormatBool(v), true
	case nil:
		return "", true
	default:
		return "", false
	}
}

func validateFormConditionReferences(raw json.RawMessage, fieldIDs map[string]struct{}, path string) error {
	if len(raw) == 0 {
		return nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("%s must be valid JSON: %w", path, err)
	}

	if conditionsRaw, ok := payload["conditions"]; ok {
		var group formSchemaConditionGroupPayload
		if err := json.Unmarshal(raw, &group); err != nil {
			return fmt.Errorf("%s must be a valid condition group: %w", path, err)
		}
		for index, child := range group.Conditions {
			if err := validateFormConditionReferences(
				child,
				fieldIDs,
				fmt.Sprintf("%s.conditions[%d]", path, index),
			); err != nil {
				return err
			}
		}
		if len(group.Conditions) == 0 && len(conditionsRaw) > 0 {
			return fmt.Errorf("%s.conditions must contain at least one condition", path)
		}
		return nil
	}

	var condition formSchemaConditionPayload
	if err := json.Unmarshal(raw, &condition); err != nil {
		return fmt.Errorf("%s must be a valid condition: %w", path, err)
	}

	fieldID := resolveFormConditionFieldID(condition)
	if fieldID == "" {
		return fmt.Errorf("%s.fieldId is required", path)
	}
	if _, ok := fieldIDs[fieldID]; !ok {
		return fmt.Errorf("%s.fieldId %q does not exist in schema", path, fieldID)
	}

	return nil
}

func validateCanonicalFormSchema(raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("schema is required")
	}

	var schema formSchemaPayload
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("schema must be valid JSON: %w", err)
	}

	if strings.TrimSpace(schema.ID) == "" {
		return fmt.Errorf("schema.id is required")
	}
	if len(schema.Steps) == 0 {
		return fmt.Errorf("schema.steps must contain at least one step")
	}

	validator := canonicalFormSchemaValidator{
		stepIDs:      make(map[string]struct{}, len(schema.Steps)),
		fieldIDs:     make(map[string]struct{}),
		optionIDs:    make(map[string]struct{}),
		validatorIDs: make(map[string]struct{}),
		fieldKeys:    make(map[string]struct{}),
	}
	if err := validator.validateStructure(schema.Steps); err != nil {
		return err
	}
	return validator.validateConditionReferences(schema.Steps)
}

type canonicalFormSchemaValidator struct {
	stepIDs      map[string]struct{}
	fieldIDs     map[string]struct{}
	optionIDs    map[string]struct{}
	validatorIDs map[string]struct{}
	fieldKeys    map[string]struct{}
}

func (validator *canonicalFormSchemaValidator) validateStructure(
	steps []formSchemaStepPayload,
) error {
	for stepIndex, step := range steps {
		if err := validator.validateStep(stepIndex, step); err != nil {
			return err
		}
	}
	return nil
}

func (validator *canonicalFormSchemaValidator) validateStep(
	stepIndex int,
	step formSchemaStepPayload,
) error {
	stepID := step.ID
	if err := validateFormLocaleStableID(stepID, fmt.Sprintf("schema.steps[%d].id", stepIndex)); err != nil {
		return err
	}
	if _, exists := validator.stepIDs[stepID]; exists {
		return fmt.Errorf("schema.steps[%d].id %q must be unique", stepIndex, stepID)
	}
	validator.stepIDs[stepID] = struct{}{}
	for fieldIndex, field := range step.Fields {
		if err := validator.validateField(stepIndex, fieldIndex, field); err != nil {
			return err
		}
	}
	return nil
}

func (validator *canonicalFormSchemaValidator) validateField(
	stepIndex int,
	fieldIndex int,
	field formSchemaFieldPayload,
) error {
	fieldID := field.ID
	if err := validateFormLocaleStableID(fieldID, fmt.Sprintf("schema.steps[%d].fields[%d].id", stepIndex, fieldIndex)); err != nil {
		return err
	}
	if _, exists := validator.fieldIDs[fieldID]; exists {
		return fmt.Errorf(
			"schema.steps[%d].fields[%d].id %q must be unique",
			stepIndex,
			fieldIndex,
			fieldID,
		)
	}
	validator.fieldIDs[fieldID] = struct{}{}
	if err := validator.validateFieldKey(stepIndex, fieldIndex, field); err != nil {
		return err
	}
	if err := validateFormFieldType(stepIndex, fieldIndex, field.Type); err != nil {
		return err
	}
	if err := validator.validateFormFieldOptions(stepIndex, fieldIndex, field.Options); err != nil {
		return err
	}
	return validator.validateFormFieldValidators(stepIndex, fieldIndex, field.Validation.Validators)
}

func validateFormLocaleStableID(id, path string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s is required", path)
	}
	if err := intrav1.ValidateFormLocaleStableID(id); err != nil {
		return fmt.Errorf("%s %q is invalid: %w", path, id, err)
	}
	return nil
}

func (validator *canonicalFormSchemaValidator) validateFieldKey(
	stepIndex int,
	fieldIndex int,
	field formSchemaFieldPayload,
) error {
	fieldKey := resolveFormFieldKey(field)
	if fieldKey == "" {
		return fmt.Errorf("schema.steps[%d].fields[%d].key is required", stepIndex, fieldIndex)
	}
	if strings.HasPrefix(fieldKey, formReservedSubmissionMetadataPrefix) {
		return fmt.Errorf(
			"schema.steps[%d].fields[%d].key %q is reserved for server-managed form submission metadata",
			stepIndex,
			fieldIndex,
			fieldKey,
		)
	}
	if _, exists := validator.fieldKeys[fieldKey]; exists {
		return fmt.Errorf(
			"schema.steps[%d].fields[%d].key %q must be unique",
			stepIndex,
			fieldIndex,
			fieldKey,
		)
	}
	validator.fieldKeys[fieldKey] = struct{}{}
	return nil
}

func validateFormFieldType(stepIndex, fieldIndex int, fieldType string) error {
	if strings.TrimSpace(fieldType) == "" {
		return fmt.Errorf("schema.steps[%d].fields[%d].type is required", stepIndex, fieldIndex)
	}
	if _, ok := allowedFormFieldTypes[fieldType]; !ok {
		return fmt.Errorf(
			"schema.steps[%d].fields[%d].type %q is not supported",
			stepIndex,
			fieldIndex,
			fieldType,
		)
	}
	return nil
}

func (validator *canonicalFormSchemaValidator) validateFormFieldOptions(
	stepIndex int,
	fieldIndex int,
	options []formSchemaOptionPayload,
) error {
	optionValues := make(map[string]struct{}, len(options))
	for optionIndex, option := range options {
		path := fmt.Sprintf("schema.steps[%d].fields[%d].options[%d].id", stepIndex, fieldIndex, optionIndex)
		if err := validateFormLocaleStableID(option.ID, path); err != nil {
			return err
		}
		if _, exists := validator.optionIDs[option.ID]; exists {
			return fmt.Errorf("%s %q must be unique across the form", path, option.ID)
		}
		validator.optionIDs[option.ID] = struct{}{}
		if err := validateFormOptionValue(stepIndex, fieldIndex, optionIndex, option.Value, optionValues); err != nil {
			return err
		}
	}
	return nil
}

func (validator *canonicalFormSchemaValidator) validateFormFieldValidators(
	stepIndex int,
	fieldIndex int,
	validators []formFieldValidatorPayload,
) error {
	for validatorIndex, formValidator := range validators {
		path := fmt.Sprintf("schema.steps[%d].fields[%d].validation.validators[%d].id", stepIndex, fieldIndex, validatorIndex)
		if err := validateFormLocaleStableID(formValidator.ID, path); err != nil {
			return err
		}
		if _, exists := validator.validatorIDs[formValidator.ID]; exists {
			return fmt.Errorf("%s %q must be unique across the form", path, formValidator.ID)
		}
		validator.validatorIDs[formValidator.ID] = struct{}{}
	}
	return nil
}

func validateFormOptionValue(
	stepIndex int,
	fieldIndex int,
	optionIndex int,
	value structured.Value, seen map[string]struct{},
) error {
	valueKey, ok := NormalizeFormOptionValueKey(value)
	if !ok {
		return fmt.Errorf(
			"schema.steps[%d].fields[%d].options[%d].value must be a string, number, or boolean",
			stepIndex,
			fieldIndex,
			optionIndex,
		)
	}
	if valueKey == "" {
		return nil
	}
	if _, exists := seen[valueKey]; exists {
		return fmt.Errorf(
			"schema.steps[%d].fields[%d].options[%d].value must be unique",
			stepIndex,
			fieldIndex,
			optionIndex,
		)
	}
	seen[valueKey] = struct{}{}
	return nil
}

func (validator *canonicalFormSchemaValidator) validateConditionReferences(
	steps []formSchemaStepPayload,
) error {
	for stepIndex, step := range steps {
		if err := validateFormConditionReferences(
			step.Condition,
			validator.fieldIDs,
			fmt.Sprintf("schema.steps[%d].condition", stepIndex),
		); err != nil {
			return err
		}
		for fieldIndex, field := range step.Fields {
			if err := validateFormConditionReferences(
				field.Condition,
				validator.fieldIDs,
				fmt.Sprintf("schema.steps[%d].fields[%d].condition", stepIndex, fieldIndex),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeCollaborativeFormSchemaPatch(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if err := validateCanonicalFormSchema(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func extractFormFieldMetadata(raw []byte) formSchemaMetadata {
	var schema formSchemaPayload
	if err := json.Unmarshal(raw, &schema); err != nil {
		return formSchemaMetadata{FieldLabels: map[string]string{}}
	}

	orderedKeys := make([]string, 0)
	seenKeys := make(map[string]struct{})
	labels := make(map[string]string)
	for _, step := range schema.Steps {
		for _, field := range step.Fields {
			fieldKey := resolveFormFieldKey(field)
			fieldLabel := resolveFormFieldLabel(field)
			if fieldKey != "" {
				if _, exists := seenKeys[fieldKey]; !exists {
					orderedKeys = append(orderedKeys, fieldKey)
					seenKeys[fieldKey] = struct{}{}
				}
				labels[fieldKey] = fieldLabel
			}
			if field.Name != "" {
				labels[field.Name] = fieldLabel
			}
			if field.ID != "" {
				labels[field.ID] = fieldLabel
			}
		}
	}

	return formSchemaMetadata{
		OrderedFieldKeys: orderedKeys,
		FieldLabels:      labels,
	}
}

func extractFormFieldLabels(raw []byte) map[string]string {
	return extractFormFieldMetadata(raw).FieldLabels
}

func ExtractFormFieldLabels(raw []byte) map[string]string {
	return extractFormFieldLabels(raw)
}
