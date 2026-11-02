package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ListCategoriesQuery pages the categories with their published Post counts.
type ListCategoriesQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	// Limit is 100 when not positive.
	Limit  int32
	Offset int32
}

// ListCategories returns all categories with post counts
func (s *Service) ListCategories(ctx context.Context, query ListCategoriesQuery) (referencecatalog.Page[Category], error) {
	page := listPage(query.Filters, query.Sorts, query.Limit, query.Offset, 100)
	var listed referencecatalog.Page[Category]
	err := s.units.Read(ctx, func(store Store) error {
		categories, total, err := store.Categories(ctx, page)
		if err != nil {
			return err
		}
		listed = referencecatalog.Page[Category]{Items: categories, Pagination: paginationOf(page, len(categories), total)}
		return nil
	})
	return listed, err
}
