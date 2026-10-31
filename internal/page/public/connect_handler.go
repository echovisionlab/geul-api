package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Page RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedPageServiceHandler
	pages *Service
}

// NewConnectHandler returns the Connect adapter of pages.
func NewConnectHandler(pages *Service) *ConnectHandler {
	return &ConnectHandler{pages: pages}
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetPageRequest]) (*connect.Response[openv1.GetPageResponse], error) {
	response, err := h.pages.GetPage(ctx, GetPageQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.ShareToken, SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(response), nil
}
