package authentication

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
)

// ConnectHandler serves the internal gateway authorization RPC.
type ConnectHandler struct {
	intrav1connect.UnimplementedInternalGatewayAuthorizationServiceHandler
	authentication *Service
}

// NewConnectHandler returns the Connect adapter of authentication.
func NewConnectHandler(authentication *Service) *ConnectHandler {
	return &ConnectHandler{authentication: authentication}
}

func (h *ConnectHandler) AuthorizeGatewayAccess(
	ctx context.Context,
	req *connect.Request[intrav1.AuthorizeGatewayAccessRequest],
) (*connect.Response[intrav1.AuthorizeGatewayAccessResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	if err := h.authentication.AuthorizeGatewayAccess(ctx, GatewayAccessQuery{
		AccountIdentityID: req.Msg.GetAccountIdentityId(), SessionID: req.Msg.GetSessionId(), Role: req.Msg.GetRole(),
	}); err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.AuthorizeGatewayAccessResponse{}), nil
}
