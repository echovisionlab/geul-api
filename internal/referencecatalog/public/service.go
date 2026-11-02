package public

import (
	stderrors "errors"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Deps are the collaborators of the public catalog reads; all are required.
type Deps struct {
	Units mutation.UnitOfWork[Store] // referencecatalogpublicpostgres.NewUnits
}

// Service holds the public Category, Tag, Client and Map Place reads.
type Service struct {
	units mutation.UnitOfWork[Store]
}

// NewService validates deps and returns the public catalog reads.
func NewService(deps Deps) (*Service, error) {
	if deps.Units == nil {
		return nil, stderrors.New("public reference catalog dependencies are required: Units")
	}
	return &Service{units: deps.Units}, nil
}

// listPage is the page of limit, defaultLimit when not positive, at offset,
// which is at least zero.
func listPage(filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, limit, offset, defaultLimit int32) referencecatalog.ListPage {
	page := referencecatalog.ListPage{Filters: filters, Sorts: sorts, Limit: limit, Offset: offset}
	if page.Limit <= 0 {
		page.Limit = defaultLimit
	}
	if page.Offset < 0 {
		page.Offset = 0
	}
	return page
}

// paginationOf is where a page of count items sits among total matches.
func paginationOf(page referencecatalog.ListPage, count int, total int64) referencecatalog.Pagination {
	return referencecatalog.Pagination{
		Total:   int32(total),
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+int32(count) < int32(total),
	}
}
