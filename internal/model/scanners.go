// Package model provides database model types and JSONB scanning utilities.
package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// ScanJSON scans a database value into a JSON-unmarshallable target.
// This is a helper function for implementing sql.Scanner on JSONB types.
func ScanJSON(value structured.Value, target structured.Value) error {
	if value == nil {
		return nil
	}
	var bytes []byte
	switch val := value.(type) {
	case []byte:
		bytes = val
	case string:
		bytes = []byte(val)
	default:
		return fmt.Errorf("cannot scan %T into JSON type", value)
	}
	if len(bytes) == 0 {
		return nil
	}
	return json.Unmarshal(bytes, target)
}

// ValueJSON marshals a value to JSON for database storage.
// This is a helper function for implementing driver.Valuer on JSONB types.
func ValueJSON(v structured.Value) (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// ValueJSONDefault marshals a value to JSON with a default if nil.
// This is useful for JSONB columns with NOT NULL constraints.
func ValueJSONDefault(v structured.Value, defaultVal structured.Value) (driver.Value, error) {
	if v == nil {
		return json.Marshal(defaultVal)
	}
	return json.Marshal(v)
}

// JSONFields stores a JSONB object whose field types are owned by its caller.
type JSONFields structured.Fields

// Scan implements sql.Scanner
func (m *JSONFields) Scan(value structured.Value) error {
	if value == nil {
		*m = make(JSONFields)
		return nil
	}
	return ScanJSON(value, m)
}

// Value implements driver.Valuer
func (m JSONFields) Value() (driver.Value, error) {
	if m == nil {
		return ValueJSONDefault(m, structured.Fields{})
	}
	return ValueJSON(m)
}
