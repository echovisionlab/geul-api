package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Share Link RPC.
type ConnectHandler struct {
	openv1connect.UnimplementedShareLinkServiceHandler
	links *Service
}

// NewConnectHandler returns the Connect adapter of links.
func NewConnectHandler(links *Service) *ConnectHandler {
	return &ConnectHandler{links: links}
}

func (h *ConnectHandler) Validate(ctx context.Context, req *connect.Request[openv1.ValidateShareLinkRequest]) (*connect.Response[openv1.ValidateShareLinkResponse], error) {
	response, err := h.links.ValidateShareLink(ctx, ValidateShareLinkQuery{Token: req.Msg.Token, Password: req.Msg.Password})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(response), nil
}
