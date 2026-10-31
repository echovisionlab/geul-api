package public

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// CategoryService implements the public CategoryService
type CategoryService struct {
	openv1connect.UnimplementedCategoryServiceHandler
	db *gorm.DB
}

// NewCategoryService creates a new public CategoryService
func NewCategoryService(db *gorm.DB) *CategoryService {
	return &CategoryService{db: db}
}

// List returns all categories with post counts
func (s *CategoryService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListCategoriesRequest],
) (*connect.Response[openv1.ListCategoriesResponse], error) {
	type CategoryResult struct {
		ID          string
		Name        string
		Slug        string
		Description *string
		PostCount   int32
	}

	var results []CategoryResult
	var total int64

	// Base query with subquery for category listing
	baseQuery := s.db.WithContext(ctx).
		Table("category c").
		Select(`
			c.id,
			c.name,
			c.slug,
			c.description,
			COUNT(DISTINCT CASE WHEN p.status IN ? THEN p.id END) as post_count
		`, postStatusValues()).
		Joins("LEFT JOIN post_category pc ON pc.category_id = c.id").
		Joins("LEFT JOIN post p ON p.id = pc.post_id").
		Group("c.id, c.name, c.slug, c.description")

	// 1. Apply filters (currently none, but for consistency)
	query, err := categoryFilterConfig.ApplyFilters(baseQuery, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total (must use subquery approach for aggregated query)
	countQuery := s.db.WithContext(ctx).Table("(?) as subq", query)
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// 2. Apply sort
	query, err = categorySortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// 3. Apply pagination
	pagination := queryutil.GetPaginationParams(
		req.Msg.Pagination.GetLimit(),
		req.Msg.Pagination.GetOffset(),
		100, // default limit for categories
	)
	query = queryutil.ApplyPagination(query, pagination)

	if err := query.Scan(&results).Error; err != nil {
		return nil, errs.Internal(err)
	}

	categories := make([]*openv1.Category, 0, len(results))
	for _, r := range results {
		categories = append(categories, &openv1.Category{
			Id:          r.ID,
			Name:        r.Name,
			Slug:        r.Slug,
			Description: r.Description,
			PostCount:   r.PostCount,
		})
	}

	return connect.NewResponse(&openv1.ListCategoriesResponse{
		Categories: categories,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(results)) < int32(total),
		},
	}), nil
}
