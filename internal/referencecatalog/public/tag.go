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

// TagService implements the public TagService
type TagService struct {
	openv1connect.UnimplementedTagServiceHandler
	db *gorm.DB
}

// NewTagService creates a new public TagService
func NewTagService(db *gorm.DB) *TagService {
	return &TagService{db: db}
}

// List returns all tags with post counts
func (s *TagService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListTagsRequest],
) (*connect.Response[openv1.ListTagsResponse], error) {
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
	query, err := tagFilterConfig.ApplyFilters(baseQuery, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total (must use subquery approach for aggregated query)
	countQuery := s.db.WithContext(ctx).Table("(?) as subq", query)
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// 2. Apply sort
	query, err = tagSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// 3. Apply pagination
	pagination := queryutil.GetPaginationParams(
		req.Msg.Pagination.GetLimit(),
		req.Msg.Pagination.GetOffset(),
		100, // default limit for tags
	)
	query = queryutil.ApplyPagination(query, pagination)

	if err := query.Scan(&results).Error; err != nil {
		return nil, errs.Internal(err)
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

	return connect.NewResponse(&openv1.ListTagsResponse{
		Tags: tags,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: pagination.Offset+int32(len(results)) < int32(total),
		},
	}), nil
}
