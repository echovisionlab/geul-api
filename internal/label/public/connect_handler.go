package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Label RPCs by translating each request
// into a query of the public Label reads.
type ConnectHandler struct {
	openv1connect.UnimplementedLabelServiceHandler
	labels *Service
}

// NewConnectHandler returns the public Connect adapter of labels.
func NewConnectHandler(labels *Service) *ConnectHandler {
	return &ConnectHandler{labels: labels}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListLabelsRequest]) (*connect.Response[openv1.ListLabelsResponse], error) {
	page, err := h.labels.List(ctx, ListLabelsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&openv1.ListLabelsResponse{Labels: page.Labels, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetLabelRequest]) (*connect.Response[openv1.GetLabelResponse], error) {
	label, err := h.labels.Get(ctx, GetLabelQuery{
		SlugOrID: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respond(&openv1.GetLabelResponse{Label: label}, err)
}
