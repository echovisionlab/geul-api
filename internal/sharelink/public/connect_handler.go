package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// ConnectHandler serves the public Share Link RPC.
type ConnectHandler struct {
	links *Service
}

// NewConnectHandler returns the Connect adapter of links.
func NewConnectHandler(links *Service) *ConnectHandler {
	return &ConnectHandler{links: links}
}

func (h *ConnectHandler) Validate(ctx context.Context, req *connect.Request[openv1.ValidateShareLinkRequest]) (*connect.Response[openv1.ValidateShareLinkResponse], error) {
	validation, err := h.links.ValidateShareLink(ctx, ValidateShareLinkQuery{Token: req.Msg.Token, Password: req.Msg.Password})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(validationMessage(validation)), nil
}

func validationMessage(validation Validation) *openv1.ValidateShareLinkResponse {
	response := &openv1.ValidateShareLinkResponse{Valid: validation.Valid, PasswordRequired: validation.PasswordRequired}
	if target := validation.Target; target != nil {
		entityType := sharelink.EntityTypeToProto(target.EntityType)
		entityID := target.EntityID
		response.EntityType = &entityType
		response.EntityId = &entityID
		response.Slug = target.Slug
	}
	return response
}
