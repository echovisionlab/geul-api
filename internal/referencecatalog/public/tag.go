package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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
func (s *Service) ListTags(ctx context.Context, query ListTagsQuery) (referencecatalog.Page[Tag], error) {
	page := listPage(query.Filters, query.Sorts, query.Limit, query.Offset, 100)
	var listed referencecatalog.Page[Tag]
	err := s.units.Read(ctx, func(store Store) error {
		tags, total, err := store.Tags(ctx, page)
		if err != nil {
			return err
		}
		listed = referencecatalog.Page[Tag]{Items: tags, Pagination: paginationOf(page, len(tags), total)}
		return nil
	})
	return listed, err
}
