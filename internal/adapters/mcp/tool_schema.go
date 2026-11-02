package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type inputSchemaProperty struct {
	name  string
	value json.RawMessage
}

// Input annotations retain descriptions, constraints and property order. The
// registered decoder types supply JSON value kinds and nested field shapes.
func generateInputSchema(annotations json.RawMessage, inputs ...reflect.Type) (json.RawMessage, error) {
	return generateInputNode(annotations, inputs, "$")
}

func generateInputNode(raw json.RawMessage, inputs []reflect.Type, path string) (json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("true")) || bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
		return raw, nil
	}
	properties, err := readInputSchemaObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for index, property := range properties {
		var generated json.RawMessage
		switch property.name {
		case "type":
			generated, err = generateInputKind(property.value, inputs)
		case "properties":
			generated, err = generateInputProperties(property.value, inputs, path)
		case "items":
			elements := inputElementTypes(inputs)
			if len(elements) > 0 && !bytes.HasPrefix(bytes.TrimSpace(property.value), []byte("[")) {
				generated, err = generateInputNode(property.value, elements, path+"[]")
			}
		case "allOf", "anyOf", "oneOf":
			generated, err = generateInputAlternatives(property.value, inputs, path)
		}
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", path, property.name, err)
		}
		if generated != nil {
			properties[index].value = generated
		}
	}
	return writeInputSchemaObject(properties), nil
}

func generateInputAlternatives(raw json.RawMessage, inputs []reflect.Type, path string) (json.RawMessage, error) {
	var alternatives []json.RawMessage
	if err := json.Unmarshal(raw, &alternatives); err != nil {
		return nil, err
	}
	for index, alternative := range alternatives {
		generated, err := generateInputNode(alternative, inputs, path)
		alternatives[index] = generated
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(alternatives)
}

func generateInputProperties(raw json.RawMessage, inputs []reflect.Type, path string) (json.RawMessage, error) {
	properties, err := readInputSchemaObject(raw)
	if err != nil {
		return nil, err
	}
	for index, property := range properties {
		fields := inputFieldTypes(inputs, property.name)
		if len(fields) == 0 {
			return nil, fmt.Errorf("%s.%s has no registered input field", path, property.name)
		}
		if opaqueInputTypes(fields) {
			continue
		}
		generated, err := generateInputNode(property.value, fields, path+"."+property.name)
		if err != nil {
			return nil, err
		}
		properties[index].value = generated
	}
	return writeInputSchemaObject(properties), nil
}

func generateInputKind(raw json.RawMessage, inputs []reflect.Type) (json.RawMessage, error) {
	if opaqueInputTypes(inputs) {
		return raw, nil
	}
	var annotated []string
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		annotated = []string{single}
	} else if err := json.Unmarshal(raw, &annotated); err != nil {
		return nil, err
	}
	for index, kind := range annotated {
		if kind == "null" {
			continue
		}
		matched := false
		for _, input := range inputs {
			if inputJSONKind(input) == kind {
				annotated[index] = inputJSONKind(input)
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("schema kind %q does not match registered decoder", kind)
		}
	}
	if single != "" {
		return json.Marshal(annotated[0])
	}
	return json.Marshal(annotated)
}

func inputJSONKind(input reflect.Type) string {
	for input.Kind() == reflect.Pointer {
		input = input.Elem()
	}
	switch input.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Array, reflect.Slice:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		return ""
	}
}

func opaqueInputTypes(inputs []reflect.Type) bool {
	unmarshaler := reflect.TypeFor[json.Unmarshaler]()
	for _, input := range inputs {
		for input.Kind() == reflect.Pointer {
			input = input.Elem()
		}
		if input.Kind() == reflect.Interface || input.Implements(unmarshaler) || reflect.PointerTo(input).Implements(unmarshaler) {
			return true
		}
	}
	return false
}

func inputFieldTypes(inputs []reflect.Type, name string) []reflect.Type {
	var fields []reflect.Type
	for _, input := range inputs {
		for input.Kind() == reflect.Pointer {
			input = input.Elem()
		}
		if input.Kind() == reflect.Map {
			fields = append(fields, input.Elem())
			continue
		}
		if input.Kind() != reflect.Struct {
			continue
		}
		for index := 0; index < input.NumField(); index++ {
			field := input.Field(index)
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if field.Anonymous && tag == "" {
				fields = append(fields, inputFieldTypes([]reflect.Type{field.Type}, name)...)
				continue
			}
			if field.PkgPath != "" {
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			if tag == name {
				fields = append(fields, field.Type)
			}
		}
	}
	return fields
}

func inputElementTypes(inputs []reflect.Type) []reflect.Type {
	var elements []reflect.Type
	for _, input := range inputs {
		for input.Kind() == reflect.Pointer {
			input = input.Elem()
		}
		if input.Kind() == reflect.Array || input.Kind() == reflect.Slice {
			elements = append(elements, input.Elem())
		}
	}
	return elements
}

func readInputSchemaObject(raw json.RawMessage) ([]inputSchemaProperty, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("expected schema object")
	}
	var properties []inputSchemaProperty
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		properties = append(properties, inputSchemaProperty{name: name.(string), value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return properties, nil
}

func writeInputSchemaObject(properties []inputSchemaProperty) json.RawMessage {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, property := range properties {
		if index > 0 {
			output.WriteByte(',')
		}
		name, _ := json.Marshal(property.name)
		output.Write(name)
		output.WriteByte(':')
		output.Write(property.value)
	}
	output.WriteByte('}')
	return output.Bytes()
}
