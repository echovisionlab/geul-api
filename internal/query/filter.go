// Package query provides common database query utilities.
package query

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// FieldType represents the data type of a filterable field.
type FieldType int

const (
	// TypeID represents UUID/ID fields (foreign keys, primary keys).
	TypeID FieldType = iota
	// TypeText represents string/text fields.
	TypeText
	// TypeNumber represents integer/float fields.
	TypeNumber
	// TypeDate represents timestamp/date fields.
	TypeDate
	// TypeBool represents boolean fields.
	TypeBool
	// TypeEnum represents enum fields (stored as text but with fixed values).
	TypeEnum
)

// FieldDef defines a filterable field's configuration.
type FieldDef struct {
	// Column is the actual database column name.
	Column string
	// Type is the field's data type.
	Type FieldType
	// AllowedOps is the list of allowed filter operations for this field.
	AllowedOps []commonv1.FilterOp
	// IsFK indicates if this is a foreign key (requires UUID validation).
	IsFK bool
	// EnumValues is the list of allowed values for enum fields.
	EnumValues []string
	// SearchColumns is a list of columns to search across (for full-text search fields).
	// When set, ILIKE is applied to all columns with OR.
	SearchColumns []string
}

// FilterConfig defines allowed filter fields for an entity.
type FilterConfig struct {
	// Fields maps request field names to their definitions.
	Fields map[string]FieldDef
	// DefaultFilters are applied when the user doesn't provide a filter for that field.
	// Useful for public APIs that should default to status != draft.
	DefaultFilters []*commonv1.FilterSpec
}

// ApplyFilters applies filters to a query based on the request.
// Returns an error if validation fails.
func (c *FilterConfig) ApplyFilters(q *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	// Apply default filters first (only if user didn't provide a filter for that field)
	for _, df := range c.DefaultFilters {
		if df == nil {
			continue
		}
		overridden := false
		for _, f := range filters {
			if f != nil && f.GetField() == df.GetField() {
				overridden = true
				break
			}
		}
		if !overridden {
			def, ok := c.Fields[df.GetField()]
			if ok {
				var err error
				q, err = c.applyFilter(q, def, df)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	// Apply user-provided filters
	for _, f := range filters {
		if f == nil {
			continue
		}

		def, ok := c.Fields[f.GetField()]
		if !ok {
			return nil, errs.InvalidFilterField(f.GetField())
		}

		if !c.isOpAllowed(def, f.GetOp()) {
			return nil, errs.InvalidFilterOp(f.GetField(), f.GetOp().String())
		}

		// Handle SearchColumns (full-text search across multiple columns)
		if len(def.SearchColumns) > 0 {
			q = c.applySearchFilter(q, def, f)
			continue
		}

		var err error
		q, err = c.applyFilter(q, def, f)
		if err != nil {
			return nil, err
		}
	}
	return q, nil
}

// applySearchFilter applies a search filter across multiple columns.
func (c *FilterConfig) applySearchFilter(q *gorm.DB, def FieldDef, f *commonv1.FilterSpec) *gorm.DB {
	value := f.GetValue()
	if value == "" {
		return q
	}

	pattern := "%" + EscapeILIKEPattern(value) + "%"
	conditions := make([]string, len(def.SearchColumns))
	args := make(structured.Values, len(def.SearchColumns))

	for i, col := range def.SearchColumns {
		conditions[i] = col + " ILIKE ?"
		args[i] = pattern
	}

	return q.Where(strings.Join(conditions, " OR "), args...)
}

// isOpAllowed checks if the operation is allowed for the field.
func (c *FilterConfig) isOpAllowed(def FieldDef, op commonv1.FilterOp) bool {
	return slices.Contains(def.AllowedOps, op)
}

// applyFilter applies a single filter to the query.
func (c *FilterConfig) applyFilter(q *gorm.DB, def FieldDef, f *commonv1.FilterSpec) (*gorm.DB, error) {
	op := f.GetOp()
	col := def.Column

	// Handle NULL checks first (no value needed)
	switch op {
	case commonv1.FilterOp_FILTER_OP_IS_NULL:
		return q.Where(col + " IS NULL"), nil
	case commonv1.FilterOp_FILTER_OP_IS_NOT_NULL:
		return q.Where(col + " IS NOT NULL"), nil
	}

	// Handle IN/NOT_IN (multiple values)
	if op == commonv1.FilterOp_FILTER_OP_IN || op == commonv1.FilterOp_FILTER_OP_NOT_IN {
		return c.applyInFilter(q, def, f)
	}

	// Handle single value operations
	value := f.GetValue()
	if value == "" {
		return nil, errs.InvalidArgument(f.GetField(), "value is required")
	}

	// Validate and convert value based on type
	parsedValue, err := c.parseValue(def, f.GetField(), value)
	if err != nil {
		return nil, err
	}

	// Apply the filter
	switch op {
	case commonv1.FilterOp_FILTER_OP_EQ:
		return q.Where(col+" = ?", parsedValue), nil
	case commonv1.FilterOp_FILTER_OP_NEQ:
		return q.Where(col+" != ?", parsedValue), nil
	case commonv1.FilterOp_FILTER_OP_GT:
		return q.Where(col+" > ?", parsedValue), nil
	case commonv1.FilterOp_FILTER_OP_GTE:
		return q.Where(col+" >= ?", parsedValue), nil
	case commonv1.FilterOp_FILTER_OP_LT:
		return q.Where(col+" < ?", parsedValue), nil
	case commonv1.FilterOp_FILTER_OP_LTE:
		return q.Where(col+" <= ?", parsedValue), nil
	case commonv1.FilterOp_FILTER_OP_LIKE:
		escaped := EscapeILIKEPattern(value)
		return q.Where(col+" LIKE ?", "%"+escaped+"%"), nil
	case commonv1.FilterOp_FILTER_OP_ILIKE:
		escaped := EscapeILIKEPattern(value)
		return q.Where(col+" ILIKE ?", "%"+escaped+"%"), nil
	default:
		return nil, errs.InvalidFilterOp(f.GetField(), op.String())
	}
}

// applyInFilter handles IN and NOT_IN operations.
func (c *FilterConfig) applyInFilter(q *gorm.DB, def FieldDef, f *commonv1.FilterSpec) (*gorm.DB, error) {
	values := f.GetValues()
	if len(values) == 0 {
		return nil, errs.InvalidArgument(f.GetField(), "values required for IN/NOT_IN")
	}

	parsedValues := make(structured.Values, len(values))
	for i, v := range values {
		parsed, err := c.parseValue(def, f.GetField(), v)
		if err != nil {
			return nil, err
		}
		parsedValues[i] = parsed
	}

	col := def.Column
	if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
		return q.Where(col+" IN ?", parsedValues), nil
	}
	return q.Where(col+" NOT IN ?", parsedValues), nil
}

// parseValue validates and converts a string value based on field type.
func (c *FilterConfig) parseValue(def FieldDef, field, value string) (structured.Value, error) {
	switch def.Type {
	case TypeID:
		if def.IsFK || strings.HasSuffix(field, "_id") || field == "id" {
			if _, err := uuid.Parse(value); err != nil {
				return nil, errs.InvalidArgument(field, "invalid UUID format")
			}
		}
		return value, nil

	case TypeText:
		return value, nil

	case TypeNumber:
		// Try int first, then float
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i, nil
		}
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f, nil
		}
		return nil, errs.InvalidArgument(field, "invalid number format")

	case TypeDate:
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			// Try date-only format
			t, err = time.Parse("2006-01-02", value)
			if err != nil {
				return nil, errs.InvalidArgument(field, "invalid date format (use RFC3339 or YYYY-MM-DD)")
			}
		}
		return t, nil

	case TypeBool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, errs.InvalidArgument(field, "invalid boolean (use true/false)")
		}
		return b, nil

	case TypeEnum:
		// Validate against allowed values (proto enum values stored directly in DB)
		if slices.Contains(def.EnumValues, value) {
			return value, nil
		}
		return nil, errs.InvalidArgument(field, fmt.Sprintf("invalid value, allowed: %v", def.EnumValues))

	default:
		return value, nil
	}
}

// Helper to create common allowed ops for each type
var (
	// IDOps are common operations for ID fields.
	IDOps = []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_EQ,
		commonv1.FilterOp_FILTER_OP_NEQ,
		commonv1.FilterOp_FILTER_OP_IN,
		commonv1.FilterOp_FILTER_OP_NOT_IN,
	}

	// TextOps are common operations for text fields.
	TextOps = []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_EQ,
		commonv1.FilterOp_FILTER_OP_NEQ,
		commonv1.FilterOp_FILTER_OP_LIKE,
		commonv1.FilterOp_FILTER_OP_ILIKE,
		commonv1.FilterOp_FILTER_OP_IN,
		commonv1.FilterOp_FILTER_OP_NOT_IN,
	}

	// NumberOps are common operations for numeric fields.
	NumberOps = []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_EQ,
		commonv1.FilterOp_FILTER_OP_NEQ,
		commonv1.FilterOp_FILTER_OP_GT,
		commonv1.FilterOp_FILTER_OP_GTE,
		commonv1.FilterOp_FILTER_OP_LT,
		commonv1.FilterOp_FILTER_OP_LTE,
	}

	// DateOps are common operations for date/timestamp fields.
	DateOps = []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_EQ,
		commonv1.FilterOp_FILTER_OP_NEQ,
		commonv1.FilterOp_FILTER_OP_GT,
		commonv1.FilterOp_FILTER_OP_GTE,
		commonv1.FilterOp_FILTER_OP_LT,
		commonv1.FilterOp_FILTER_OP_LTE,
	}

	// BoolOps are common operations for boolean fields.
	BoolOps = []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_EQ,
		commonv1.FilterOp_FILTER_OP_NEQ,
	}

	// EnumOps are common operations for enum fields.
	EnumOps = []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_EQ,
		commonv1.FilterOp_FILTER_OP_NEQ,
		commonv1.FilterOp_FILTER_OP_IN,
		commonv1.FilterOp_FILTER_OP_NOT_IN,
	}

	// SearchOps are operations for search fields (full-text search).
	SearchOps = []commonv1.FilterOp{
		commonv1.FilterOp_FILTER_OP_ILIKE,
	}
)
