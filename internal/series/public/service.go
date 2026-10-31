package public

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// Page is the public Series query result after storage filtering and
// projection have been completed by the concrete adapter.
type Page struct {
	Items  []*openv1.Series
	Total  int32
	Limit  int32
	Offset int32
}

// Reader owns the concrete public Series query and localization projection.
type Reader interface {
	List(context.Context, *openv1.ListSeriesRequest, string) (Page, error)
	Get(context.Context, string, string) (*openv1.Series, error)
}

type SeriesService struct {
	openv1connect.UnimplementedSeriesServiceHandler
	reader Reader
}

func NewSeriesService(reader Reader) *SeriesService {
	if reader == nil {
		panic("public SeriesService: reader is required")
	}
	return &SeriesService{reader: reader}
}

func (s *SeriesService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListSeriesRequest],
) (*connect.Response[openv1.ListSeriesResponse], error) {
	page, err := s.reader.List(ctx, req.Msg, req.Header().Get("Accept-Language"))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.ListSeriesResponse{
		Series: page.Items,
		Pagination: &commonv1.PaginationResponse{
			Total:   page.Total,
			Limit:   page.Limit,
			Offset:  page.Offset,
			HasMore: page.Offset+int32(len(page.Items)) < page.Total,
		},
	}), nil
}

func (s *SeriesService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetSeriesRequest],
) (*connect.Response[openv1.GetSeriesResponse], error) {
	idOrSlug := strings.TrimSpace(req.Msg.Slug)
	if idOrSlug == "" {
		return nil, errs.Required("slug")
	}
	item, err := s.reader.Get(ctx, idOrSlug, req.Header().Get("Accept-Language"))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.GetSeriesResponse{Series: item}), nil
}
