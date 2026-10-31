package sharelink

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the Share Link management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedShareLinkServiceHandler
	links *Service
}

// NewConnectHandler returns the Connect adapter of links.
func NewConnectHandler(links *Service) *ConnectHandler {
	return &ConnectHandler{links: links}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// entityType maps the requested target kind. An unknown value keeps its
// name so the use case rejects it by name.
func entityType(value managev1.ShareLinkEntityType) EntityType {
	if stored, ok := EntityTypeFromProto(value); ok {
		return stored
	}
	return EntityType(value.String())
}

func (h *ConnectHandler) ListShareLinks(ctx context.Context, req *connect.Request[managev1.ListShareLinksRequest]) (*connect.Response[managev1.ListShareLinksResponse], error) {
	links, err := h.links.ListShareLinks(ctx, ListShareLinksQuery{EntityType: entityType(req.Msg.EntityType), EntityID: req.Msg.EntityId})
	return respond(&managev1.ListShareLinksResponse{ShareLinks: links}, err)
}

func (h *ConnectHandler) CreateShareLink(ctx context.Context, req *connect.Request[managev1.CreateShareLinkRequest]) (*connect.Response[managev1.CreateShareLinkResponse], error) {
	command := CreateShareLinkCommand{
		EntityType: entityType(req.Msg.EntityType), EntityID: req.Msg.EntityId, Label: req.Msg.Label, Password: req.Msg.Password,
	}
	if req.Msg.ExpiresAt != nil {
		expiresAt := req.Msg.ExpiresAt.AsTime()
		command.ExpiresAt = &expiresAt
	}
	link, err := h.links.CreateShareLink(ctx, command)
	return respond(&managev1.CreateShareLinkResponse{ShareLink: link}, err)
}

func (h *ConnectHandler) DeleteShareLink(ctx context.Context, req *connect.Request[managev1.DeleteShareLinkRequest]) (*connect.Response[managev1.DeleteShareLinkResponse], error) {
	err := h.links.DeleteShareLink(ctx, DeleteShareLinkCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteShareLinkResponse{Success: true}, err)
}
