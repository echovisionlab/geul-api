// Package query provides common database query utilities.
package query

import (
	"fmt"
	"strings"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// SortConfig defines allowed sort fields and default sorting.
type SortConfig struct {
	// AllowedFields maps request field names to database column names.
	// This provides a whitelist to prevent SQL injection.
	AllowedFields map[string]string
	// DefaultSort is the default ORDER BY clause when no sort is specified.
	// Should include a unique field (e.g. "created_at DESC, id ASC") for deterministic pagination.
	DefaultSort string
}

// ApplySort applies sorting to a query based on the request.
// When no sorts are specified, DefaultSort is used.
// Returns an error if an invalid sort field is requested.
func (c *SortConfig) ApplySort(q *gorm.DB, sorts []*commonv1.SortSpec) (*gorm.DB, error) {
	if len(sorts) == 0 {
		if c.DefaultSort != "" {
			q = q.Order(c.DefaultSort)
		}
		return q, nil
	}

	for _, sort := range sorts {
		col, ok := c.AllowedFields[sort.GetField()]
		if !ok {
			return nil, errs.InvalidSortField(sort.GetField())
		}
		order := "ASC"
		if sort.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC {
			order = "DESC"
		}
		q = q.Order(fmt.Sprintf("%s %s", col, order))
	}
	return q, nil
}

// PaginationParams holds pagination parameters.
type PaginationParams struct {
	Limit  int32
	Offset int32
}

// GetPaginationParams extracts pagination params with a default limit.
func GetPaginationParams(limit, offset int32, defaultLimit int32) PaginationParams {
	if limit <= 0 {
		limit = defaultLimit
	}
	if offset < 0 {
		offset = 0
	}
	return PaginationParams{Limit: limit, Offset: offset}
}

// ApplyPagination applies limit and offset to a query.
func ApplyPagination(q *gorm.DB, params PaginationParams) *gorm.DB {
	return q.Limit(int(params.Limit)).Offset(int(params.Offset))
}

// EscapeILIKEPattern escapes special ILIKE characters (%, _, \) to prevent pattern injection.
func EscapeILIKEPattern(s string) string {
	// Order matters: escape backslash first
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
