package query

import (
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// PaginationConfig defines pagination constraints for a resource.
type PaginationConfig struct {
	DefaultLimit int32 // Default limit if not specified (e.g., 20)
	MaxLimit     int32 // Maximum allowed limit to prevent DoS (e.g., 100)
}

// DefaultPaginationConfig is suitable for most resources.
var DefaultPaginationConfig = PaginationConfig{
	DefaultLimit: 20,
	MaxLimit:     100,
}

// LargePaginationConfig is for resources that typically need more items.
var LargePaginationConfig = PaginationConfig{
	DefaultLimit: 100,
	MaxLimit:     500,
}

// Pagination represents validated pagination parameters.
type Pagination struct {
	Limit  int32
	Offset int32
}

// ExtractPaginationRaw extracts and validates pagination from raw limit/offset values.
// Useful when the pagination proto message differs (e.g., openv1 vs managev1).
func ExtractPaginationRaw(limit, offset int32, config PaginationConfig) Pagination {
	p := Pagination{
		Limit:  config.DefaultLimit,
		Offset: 0,
	}

	if limit > 0 {
		p.Limit = min(limit, config.MaxLimit)
	}

	if offset > 0 {
		p.Offset = offset
	}

	return p
}

// ExtractPagination extracts and validates pagination from a proto request.
// Uses the provided config for defaults and maximum limits.
func ExtractPagination(req *commonv1.PaginationRequest, config PaginationConfig) Pagination {
	if req == nil {
		return ExtractPaginationRaw(0, 0, config)
	}
	return ExtractPaginationRaw(req.Limit, req.Offset, config)
}

// NormalizePaginationParams returns the API's default pagination limit and
// offset for call sites that consume the values separately.
func NormalizePaginationParams(req *commonv1.PaginationRequest) (int32, int32) {
	pagination := ExtractPagination(req, DefaultPaginationConfig)
	return pagination.Limit, pagination.Offset
}

// Apply applies pagination to a GORM query.
func (p Pagination) Apply(q *gorm.DB) *gorm.DB {
	return q.Limit(int(p.Limit)).Offset(int(p.Offset))
}

// BuildResponse creates a PaginationResponse for the result.
func (p Pagination) BuildResponse(total int64) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total:   int32(total),
		Limit:   p.Limit,
		Offset:  p.Offset,
		HasMore: p.Offset+p.Limit < int32(total),
	}
}

// HasMore returns true if there are more items after the current page.
func (p Pagination) HasMore(total int64) bool {
	return p.Offset+p.Limit < int32(total)
}
