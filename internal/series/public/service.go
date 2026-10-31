package public

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Page is the public Series query result after storage filtering and
// projection have been completed by the concrete adapter.
type Page struct {
	Items  []*openv1.Series
	Total  int32
	Limit  int32
	Offset int32
}

// ListSeriesQuery lists published Series. A zero Limit reads the default
// page; AcceptLanguage picks their localization.
type ListSeriesQuery struct {
	Limit          int32
	Offset         int32
	Filters        []*commonv1.FilterSpec
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

// GetSeriesQuery reads one published Series by ID or slug.
type GetSeriesQuery struct {
	IDOrSlug       string
	AcceptLanguage string
}

// Reader owns the concrete public Series query and localization projection.
type Reader interface {
	List(context.Context, ListSeriesQuery) (Page, error)
	Get(context.Context, string, string) (*openv1.Series, error)
}

// Service holds the public Series use cases.
type Service struct {
	reader Reader
}

// NewService returns the public Series use cases over reader.
func NewService(reader Reader) (*Service, error) {
	if reader == nil {
		return nil, errors.New("public series reader is required")
	}
	return &Service{reader: reader}, nil
}

func (s *Service) ListSeries(ctx context.Context, query ListSeriesQuery) (*openv1.ListSeriesResponse, error) {
	page, err := s.reader.List(ctx, query)
	if err != nil {
		return nil, err
	}
	return &openv1.ListSeriesResponse{
		Series: page.Items,
		Pagination: &commonv1.PaginationResponse{
			Total:   page.Total,
			Limit:   page.Limit,
			Offset:  page.Offset,
			HasMore: page.Offset+int32(len(page.Items)) < page.Total,
		},
	}, nil
}

func (s *Service) GetSeries(ctx context.Context, query GetSeriesQuery) (*openv1.GetSeriesResponse, error) {
	idOrSlug := strings.TrimSpace(query.IDOrSlug)
	if idOrSlug == "" {
		return nil, domainerr.Required("slug")
	}
	item, err := s.reader.Get(ctx, idOrSlug, query.AcceptLanguage)
	if err != nil {
		return nil, err
	}
	return &openv1.GetSeriesResponse{Series: item}, nil
}
