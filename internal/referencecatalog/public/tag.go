package public

import (
	"context"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// ListTagsQuery pages the tags with their published Post counts.
type ListTagsQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	// Limit is 100 when not positive.
	Limit  int32
	Offset int32
}

// ListTags returns all tags with post counts
func (s *Service) ListTags(ctx context.Context, query ListTagsQuery) (referencecatalog.Page[*openv1.Tag], error) {
	type TagResult struct {
		ID        string
		Name      string
		Slug      string
		PostCount int32
	}

	var results []TagResult
	var total int64

	// Base query with subquery for tag listing
	baseQuery := s.db.WithContext(ctx).
		Table("tag t").
		Select(`
			t.id,
			t.name,
			t.slug,
			COUNT(DISTINCT CASE WHEN p.status IN ? THEN p.id END) as post_count
		`, postStatusValues()).
		Joins("LEFT JOIN post_tag pt ON pt.tag_id = t.id").
		Joins("LEFT JOIN post p ON p.id = pt.post_id").
		Group("t.id, t.name, t.slug")

	// 1. Apply filters (currently none, but for consistency)
	scoped, err := tagFilterConfig.ApplyFilters(baseQuery, query.Filters)
	if err != nil {
		return referencecatalog.Page[*openv1.Tag]{}, err
	}

	// Count total (must use subquery approach for aggregated query)
	countQuery := s.db.WithContext(ctx).Table("(?) as subq", scoped)
	if err := countQuery.Count(&total).Error; err != nil {
		return referencecatalog.Page[*openv1.Tag]{}, err
	}

	// 2. Apply sort
	scoped, err = tagSortConfig.ApplySort(scoped, query.Sorts)
	if err != nil {
		return referencecatalog.Page[*openv1.Tag]{}, err
	}

	// 3. Apply pagination
	pagination := queryutil.GetPaginationParams(
		query.Limit,
		query.Offset,
		100, // default limit for tags
	)
	scoped = queryutil.ApplyPagination(scoped, pagination)

	if err := scoped.Scan(&results).Error; err != nil {
		return referencecatalog.Page[*openv1.Tag]{}, err
	}

	tags := make([]*openv1.Tag, 0, len(results))
	for _, r := range results {
		tags = append(tags, &openv1.Tag{
			Id:        r.ID,
			Name:      r.Name,
			Slug:      r.Slug,
			PostCount: r.PostCount,
		})
	}

	return referencecatalog.Page[*openv1.Tag]{
		Items: tags,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(results)) < int32(total),
		},
	}, nil
}
