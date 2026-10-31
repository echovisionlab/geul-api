package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Work RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedWorkServiceHandler
	works *Service
}

// NewConnectHandler returns the Connect adapter of works.
func NewConnectHandler(works *Service) *ConnectHandler {
	return &ConnectHandler{works: works}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetWorkRequest]) (*connect.Response[openv1.GetWorkResponse], error) {
	return respond(h.works.Get(ctx, GetWorkQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListWorksRequest]) (*connect.Response[openv1.ListWorksResponse], error) {
	return respond(h.works.List(ctx, ListWorksQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) ListMapFeatures(ctx context.Context, req *connect.Request[openv1.ListWorkMapFeaturesRequest]) (*connect.Response[openv1.ListWorkMapFeaturesResponse], error) {
	return respond(h.works.ListMapFeatures(ctx, ListWorkMapFeaturesQuery{Viewport: req.Msg.Viewport, Filters: req.Msg.Filters, Sorts: req.Msg.Sorts}))
}
