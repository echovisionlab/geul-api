package sharelink

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the Share Link management RPCs.
type ConnectHandler struct {
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

var entityTypes = map[EntityType]managev1.ShareLinkEntityType{
	EntityPost:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST,
	EntityPage:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE,
	EntityWork:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK,
	EntityRelease:       managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE,
	EntityForm:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM,
	EntityFormDashboard: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD,
	EntityPrivacy:       managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY,
	EntityTerms:         managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS,
	EntityLabel:         managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
	EntityArtist:        managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST,
}

// EntityTypeToProto maps a stored entity type to the API enum. A value the
// API does not know is UNSPECIFIED.
func EntityTypeToProto(entityType EntityType) managev1.ShareLinkEntityType {
	return entityTypes[entityType]
}

// EntityTypeFromProto maps an API entity type to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func EntityTypeFromProto(entityType managev1.ShareLinkEntityType) (EntityType, bool) {
	for stored, value := range entityTypes {
		if value == entityType {
			return stored, true
		}
	}
	return "", false
}

// entityType maps the requested target kind. An unknown value keeps its
// name so the use case rejects it by name.
func entityType(value managev1.ShareLinkEntityType) EntityType {
	if stored, ok := EntityTypeFromProto(value); ok {
		return stored
	}
	return EntityType(value.String())
}

func linkItem(link Link) *managev1.ShareLinkItem {
	return &managev1.ShareLinkItem{
		Id: link.ID, EntityType: EntityTypeToProto(link.EntityType), EntityId: link.EntityID, Token: link.Token,
		Url: fmt.Sprintf("/s/%s", link.Token), CreatedAt: timestamppb.New(link.CreatedAt), ExpiresAt: timestamppb.New(link.ExpiresAt),
		HasPassword: link.PasswordHash != nil, Label: link.Label,
	}
}

func (h *ConnectHandler) ListShareLinks(ctx context.Context, req *connect.Request[managev1.ListShareLinksRequest]) (*connect.Response[managev1.ListShareLinksResponse], error) {
	links, err := h.links.ListShareLinks(ctx, ListShareLinksQuery{EntityType: entityType(req.Msg.EntityType), EntityID: req.Msg.EntityId})
	if err != nil {
		return respond[managev1.ListShareLinksResponse](nil, err)
	}
	items := make([]*managev1.ShareLinkItem, len(links))
	for i, link := range links {
		items[i] = linkItem(link)
	}
	return respond(&managev1.ListShareLinksResponse{ShareLinks: items}, nil)
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
	if err != nil {
		return respond[managev1.CreateShareLinkResponse](nil, err)
	}
	return respond(&managev1.CreateShareLinkResponse{ShareLink: linkItem(link)}, nil)
}

func (h *ConnectHandler) DeleteShareLink(ctx context.Context, req *connect.Request[managev1.DeleteShareLinkRequest]) (*connect.Response[managev1.DeleteShareLinkResponse], error) {
	err := h.links.DeleteShareLink(ctx, DeleteShareLinkCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteShareLinkResponse{Success: true}, err)
}
