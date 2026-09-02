package form

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type formFieldValidationPayload struct {
	Validators []formFieldValidatorPayload `json:"validators"`
}

type formFieldValidatorPayload struct {
	ID        string          `json:"id"`
	Predicate string          `json:"predicate"`
	Value     submissionValue `json:"value"`
}

type submissionSchemaField struct {
	Key           string
	Type          string
	Options       map[string]struct{}
	Validation    formFieldValidationPayload
	Condition     json.RawMessage
	StepCondition json.RawMessage
}

// ValidateFormSubmissionAgainstSchema validates a public response against the
// current source document. It intentionally does not persist a schema snapshot.
func ValidateFormSubmissionAgainstSchema(rawSchema, rawSubmission []byte) error {
	var schema formSchemaPayload
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return fmt.Errorf("invalid current form schema: %w", err)
	}
	var values submissionObject
	if err := json.Unmarshal(rawSubmission, &values); err != nil || values == nil {
		return fmt.Errorf("submission must be a JSON object")
	}

	fields, fieldKeyByID := compileSubmissionSchema(schema)
	if err := rejectUnknownSubmissionFields(values, fields); err != nil {
		return err
	}
	for _, field := range fields {
		if !submissionFieldActive(field, values, fieldKeyByID) {
			continue
		}
		value, present := values[field.Key]
		if err := validateSubmissionField(field, value, present); err != nil {
			return fmt.Errorf("field %q: %w", field.Key, err)
		}
	}
	return nil
}

func compileSubmissionSchema(schema formSchemaPayload) (map[string]submissionSchemaField, map[string]string) {
	fields := make(map[string]submissionSchemaField)
	fieldKeyByID := make(map[string]string)
	for _, step := range schema.Steps {
		for _, field := range step.Fields {
			fieldKeyByID[field.ID] = resolveFormFieldKey(field)
		}
	}
	for _, step := range schema.Steps {
		for _, field := range step.Fields {
			key := resolveFormFieldKey(field)
			options := make(map[string]struct{}, len(field.Options))
			for _, option := range field.Options {
				encoded, _ := json.Marshal(option.Value)
				options[string(encoded)] = struct{}{}
			}
			fields[key] = submissionSchemaField{
				Key:           key,
				Type:          field.Type,
				Options:       options,
				Validation:    field.Validation,
				Condition:     field.Condition,
				StepCondition: step.Condition,
			}
		}
	}
	return fields, fieldKeyByID
}

func rejectUnknownSubmissionFields(values submissionObject, fields map[string]submissionSchemaField) error {
	for key := range values {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

func submissionFieldActive(field submissionSchemaField, values submissionObject, fieldKeyByID map[string]string) bool {
	return submissionConditionActive(field.StepCondition, values, fieldKeyByID) &&
		submissionConditionActive(field.Condition, values, fieldKeyByID)
}

func submissionConditionActive(raw json.RawMessage, values submissionObject, fieldKeyByID map[string]string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return false
	}
	if childrenRaw, ok := node["conditions"]; ok {
		return submissionConditionGroupActive(node, childrenRaw, values, fieldKeyByID)
	}
	return submissionConditionLeafActive(node, values, fieldKeyByID)
}

func submissionConditionGroupActive(
	node map[string]json.RawMessage,
	childrenRaw json.RawMessage,
	values submissionObject,
	fieldKeyByID map[string]string,
) bool {
	var children []json.RawMessage
	if err := json.Unmarshal(childrenRaw, &children); err != nil {
		return false
	}
	var logic string
	_ = json.Unmarshal(node["logic"], &logic)
	for _, child := range children {
		active := submissionConditionActive(child, values, fieldKeyByID)
		if logic == "or" && active {
			return true
		}
		if logic != "or" && !active {
			return false
		}
	}
	return logic != "or"
}

func submissionConditionLeafActive(
	node map[string]json.RawMessage,
	values submissionObject,
	fieldKeyByID map[string]string,
) bool {
	var fieldID, legacyField, operator string
	var expected submissionValue
	_ = json.Unmarshal(node["fieldId"], &fieldID)
	_ = json.Unmarshal(node["field"], &legacyField)
	_ = json.Unmarshal(node["operator"], &operator)
	_ = json.Unmarshal(node["value"], &expected)
	if fieldID == "" {
		fieldID = legacyField
	}
	key := fieldKeyByID[fieldID]
	actual, exists := values[key]
	switch operator {
	case "eq":
		return fmt.Sprint(actual) == fmt.Sprint(expected)
	case "neq":
		return fmt.Sprint(actual) != fmt.Sprint(expected)
	case "exists":
		return exists && actual != nil && fmt.Sprint(actual) != ""
	case "gt", "gte", "lt", "lte":
		left, leftOK := conditionComparableValue(actual)
		right, rightOK := conditionComparableValue(expected)
		if !leftOK || !rightOK {
			return false
		}
		return numericPredicateSatisfied(operator, left, right)
	case "in", "notIn":
		items, ok := expected.(submissionValues)
		contained := ok && containsSubmissionValue(items, actual)
		return contained == (operator == "in")
	case "contains", "containsAny", "containsAll":
		return collectionPredicateSatisfied(operator, actual, expected)
	default:
		return false
	}
}

func collectionPredicateSatisfied(operator string, actual, expected submissionValue) bool {
	items, ok := actual.(submissionValues)
	if !ok {
		return false
	}
	if operator == "contains" {
		return containsSubmissionValue(items, expected)
	}
	expectedItems, ok := expected.(submissionValues)
	if !ok {
		return false
	}
	for _, item := range expectedItems {
		contained := containsSubmissionValue(items, item)
		if operator == "containsAny" && contained {
			return true
		}
		if operator == "containsAll" && !contained {
			return false
		}
	}
	return operator == "containsAll"
}

func containsSubmissionValue(items submissionValues, expected submissionValue) bool {
	for _, item := range items {
		if fmt.Sprint(item) == fmt.Sprint(expected) {
			return true
		}
	}
	return false
}

func conditionComparableValue(value submissionValue) (float64, bool) {
	if number, ok := value.(float64); ok {
		return number, true
	}
	if text, ok := value.(string); ok {
		parsed, err := time.Parse("2006-01-02", text)
		if err == nil {
			return float64(parsed.Unix()), true
		}
	}
	return 0, false
}

func validateSubmissionField(field submissionSchemaField, value submissionValue, present bool) error {
	required := submissionFieldRequired(field.Validation.Validators)
	if !present || value == nil {
		if required {
			return fmt.Errorf("is required")
		}
		return nil
	}

	if err := validateSubmissionValue(field, value, required); err != nil {
		return err
	}
	for _, validator := range field.Validation.Validators {
		if validator.Predicate == "required" {
			continue
		}
		if err := applySubmissionValidator(field.Type, value, validator); err != nil {
			return err
		}
	}
	return nil
}

func submissionFieldRequired(validators []formFieldValidatorPayload) bool {
	for _, validator := range validators {
		if validator.Predicate == "required" {
			return true
		}
	}
	return false
}

func validateSubmissionValue(field submissionSchemaField, value submissionValue, required bool) error {
	switch field.Type {
	case "text", "textarea", "tel", "email", "select", "date":
		return validateSubmissionTextValue(field, value, required)
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("must be a number")
		}
	case "multiselect":
		return validateSubmissionMultiselectValue(field.Options, value, required)
	case "checkbox", "switch":
		return validateSubmissionBooleanValue(field.Type, value, required)
	default:
		return fmt.Errorf("has unsupported type %q", field.Type)
	}
	return nil
}

func validateSubmissionTextValue(field submissionSchemaField, value submissionValue, required bool) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("must be a string")
	}
	if text == "" {
		if required {
			return fmt.Errorf("is required")
		}
		return nil
	}
	if field.Type == "email" {
		address, err := mail.ParseAddress(text)
		if err != nil || address.Address != text || !strings.Contains(text, "@") {
			return fmt.Errorf("must be a valid email")
		}
	}
	if field.Type == "date" {
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return fmt.Errorf("must be an ISO date")
		}
	}
	if field.Type == "select" && !optionAllowed(field.Options, value) {
		return fmt.Errorf("must be an available option")
	}
	return nil
}

func validateSubmissionMultiselectValue(options map[string]struct{}, value submissionValue, required bool) error {
	items, ok := value.(submissionValues)
	if !ok {
		return fmt.Errorf("must be an array")
	}
	if required && len(items) == 0 {
		return fmt.Errorf("is required")
	}
	for _, item := range items {
		if !optionAllowed(options, item) {
			return fmt.Errorf("contains an unavailable option")
		}
	}
	return nil
}

func validateSubmissionBooleanValue(fieldType string, value submissionValue, required bool) error {
	checked, ok := value.(bool)
	if !ok {
		return fmt.Errorf("must be a boolean")
	}
	if fieldType == "checkbox" && required && !checked {
		return fmt.Errorf("must be checked")
	}
	return nil
}

func optionAllowed(options map[string]struct{}, value submissionValue) bool {
	encoded, _ := json.Marshal(value)
	_, ok := options[string(encoded)]
	return ok
}

func applySubmissionValidator(fieldType string, value submissionValue, validator formFieldValidatorPayload) error {
	switch validator.Predicate {
	case "gt", "gte", "lt", "lte", "eq":
		return applyComparableSubmissionValidator(fieldType, value, validator)
	case "url":
		return validateSubmissionURL(value)
	case "regex":
		return applySubmissionRegexValidator(value, validator.Value)
	case "email":
		// The field type check already validates email syntax.
	case "minDate", "maxDate":
		return applySubmissionDateBoundaryValidator(value, validator)
	case "futureDate", "pastDate", "weekdayOnly", "minAge", "maxAge":
		return applySubmissionRelativeDateValidator(value, validator, time.Now())
	}
	return nil
}

func applyComparableSubmissionValidator(
	fieldType string,
	value submissionValue,
	validator formFieldValidatorPayload,
) error {
	actual, ok := comparableSubmissionValue(fieldType, value)
	if !ok {
		return fmt.Errorf("cannot apply %s validator", validator.Predicate)
	}
	expected, err := strconv.ParseFloat(fmt.Sprint(validator.Value), 64)
	if err != nil {
		return fmt.Errorf("has invalid %s validator", validator.Predicate)
	}
	if !numericPredicateSatisfied(validator.Predicate, actual, expected) {
		return fmt.Errorf("does not satisfy %s", validator.Predicate)
	}
	return nil
}

func numericPredicateSatisfied(predicate string, actual, expected float64) bool {
	switch predicate {
	case "gt":
		return actual > expected
	case "gte":
		return actual >= expected
	case "lt":
		return actual < expected
	case "lte":
		return actual <= expected
	case "eq":
		return actual == expected
	default:
		return false
	}
}

func validateSubmissionURL(value submissionValue) error {
	text, _ := value.(string)
	parsed, err := url.ParseRequestURI(text)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must be a valid URL")
	}
	return nil
}

func applySubmissionRegexValidator(value, rawPattern submissionValue) error {
	pattern, ok := rawPattern.(string)
	if !ok {
		return fmt.Errorf("has invalid regex validator")
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil || !compiled.MatchString(fmt.Sprint(value)) {
		return fmt.Errorf("does not match required format")
	}
	return nil
}

func applySubmissionDateBoundaryValidator(value submissionValue, validator formFieldValidatorPayload) error {
	actual, ok := value.(string)
	expected, expectedOK := validator.Value.(string)
	if !ok || !expectedOK {
		return fmt.Errorf("has invalid date validator")
	}
	if (validator.Predicate == "minDate" && actual < expected) ||
		(validator.Predicate == "maxDate" && actual > expected) {
		return fmt.Errorf("does not satisfy %s", validator.Predicate)
	}
	return nil
}

func applySubmissionRelativeDateValidator(
	value submissionValue,
	validator formFieldValidatorPayload,
	now time.Time,
) error {
	text, ok := value.(string)
	date, err := time.Parse("2006-01-02", text)
	if !ok || err != nil {
		return fmt.Errorf("has invalid date value")
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch validator.Predicate {
	case "futureDate":
		if !date.After(today) {
			return fmt.Errorf("must be a future date")
		}
	case "pastDate":
		if !date.Before(today) {
			return fmt.Errorf("must be a past date")
		}
	case "weekdayOnly":
		if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
			return fmt.Errorf("must be a weekday")
		}
	case "minAge", "maxAge":
		return validateSubmissionAge(date, today, validator)
	}
	return nil
}

func validateSubmissionAge(date, today time.Time, validator formFieldValidatorPayload) error {
	expected, err := strconv.Atoi(fmt.Sprint(validator.Value))
	if err != nil {
		return fmt.Errorf("has invalid age validator")
	}
	age := today.Year() - date.Year()
	birthdayThisYear := time.Date(today.Year(), date.Month(), date.Day(), 0, 0, 0, 0, today.Location())
	if today.Before(birthdayThisYear) {
		age--
	}
	if (validator.Predicate == "minAge" && age < expected) ||
		(validator.Predicate == "maxAge" && age > expected) {
		return fmt.Errorf("does not satisfy %s", validator.Predicate)
	}
	return nil
}

func comparableSubmissionValue(fieldType string, value submissionValue) (float64, bool) {
	switch fieldType {
	case "number":
		result, ok := value.(float64)
		return result, ok
	case "multiselect":
		result, ok := value.(submissionValues)
		return float64(len(result)), ok
	default:
		result, ok := value.(string)
		return float64(len([]rune(result))), ok
	}
}
