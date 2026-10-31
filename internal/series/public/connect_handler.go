package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Series RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedSeriesServiceHandler
	series *Service
}

// NewConnectHandler returns the Connect adapter of series.
func NewConnectHandler(series *Service) *ConnectHandler {
	return &ConnectHandler{series: series}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListSeriesRequest]) (*connect.Response[openv1.ListSeriesResponse], error) {
	return respond(h.series.ListSeries(ctx, ListSeriesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetSeriesRequest]) (*connect.Response[openv1.GetSeriesResponse], error) {
	return respond(h.series.GetSeries(ctx, GetSeriesQuery{IDOrSlug: req.Msg.Slug, AcceptLanguage: req.Header().Get("Accept-Language")}))
}
