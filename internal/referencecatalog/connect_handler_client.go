package referencecatalog

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ClientConnectHandler serves the Client RPCs.
type ClientConnectHandler struct {
	managev1connect.UnimplementedClientServiceHandler
	catalog *Service
}

// NewClientConnectHandler returns the Client Connect adapter of catalog.
func NewClientConnectHandler(catalog *Service) *ClientConnectHandler {
	return &ClientConnectHandler{catalog: catalog}
}

func (h *ClientConnectHandler) GetClient(ctx context.Context, req *connect.Request[managev1.GetClientRequest]) (*connect.Response[managev1.Client], error) {
	client, err := h.catalog.GetClient(ctx, GetClientQuery{ID: req.Msg.Id})
	return respond(clientMessage(client), err)
}

func (h *ClientConnectHandler) ListClients(ctx context.Context, req *connect.Request[managev1.ListClientsRequest]) (*connect.Response[managev1.ListClientsResponse], error) {
	page, err := h.catalog.ListClients(ctx, ListClientsQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&managev1.ListClientsResponse{Clients: messages(page.Items, clientMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *ClientConnectHandler) SearchClients(ctx context.Context, req *connect.Request[managev1.SearchClientsRequest]) (*connect.Response[managev1.SearchClientsResponse], error) {
	clients, err := h.catalog.SearchClients(ctx, SearchClientsQuery{Query: req.Msg.Query, Limit: req.Msg.Limit})
	return respond(&managev1.SearchClientsResponse{Clients: messages(clients, clientMessage)}, err)
}

func (h *ClientConnectHandler) ListClientsAdmin(ctx context.Context, req *connect.Request[managev1.ListClientsAdminRequest]) (*connect.Response[managev1.ListClientsAdminResponse], error) {
	page, err := h.catalog.ListClientsAdmin(ctx, ListClientsAdminQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&managev1.ListClientsAdminResponse{Clients: messages(page.Items, clientWithStatsMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *ClientConnectHandler) CreateClient(ctx context.Context, req *connect.Request[managev1.CreateClientRequest]) (*connect.Response[managev1.Client], error) {
	client, err := h.catalog.CreateClient(ctx, CreateClientCommand{Name: req.Msg.Name, Website: req.Msg.Website})
	return respond(clientMessage(client), err)
}

func (h *ClientConnectHandler) UpdateClient(ctx context.Context, req *connect.Request[managev1.UpdateClientRequest]) (*connect.Response[managev1.Client], error) {
	client, err := h.catalog.UpdateClient(ctx, UpdateClientCommand{ID: req.Msg.Id, Name: req.Msg.Name, Website: req.Msg.Website})
	return respond(clientMessage(client), err)
}

func (h *ClientConnectHandler) DeleteClient(ctx context.Context, req *connect.Request[managev1.DeleteClientRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteClient(ctx, DeleteClientCommand{ID: req.Msg.Id}))
}

func (h *ClientConnectHandler) SetClientLogo(ctx context.Context, req *connect.Request[managev1.SetClientLogoRequest]) (*connect.Response[managev1.SetClientLogoResponse], error) {
	slot := clientLogoSlotOf(req.Msg.Variant)
	asset, err := h.catalog.SetClientLogo(ctx, SetClientLogoCommand{ClientID: req.Msg.ClientId, Slot: slot, FileID: req.Msg.FileId})
	response := &managev1.SetClientLogoResponse{}
	if slot == ClientLogoDark {
		response.LogoDarkAsset = mediaasset.ReferenceProto(asset)
	} else {
		response.LogoLightAsset = mediaasset.ReferenceProto(asset)
	}
	return respond(response, err)
}

func (h *ClientConnectHandler) DeleteClientLogo(ctx context.Context, req *connect.Request[managev1.DeleteClientLogoRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteClientLogo(ctx, DeleteClientLogoCommand{ClientID: req.Msg.ClientId, Slot: clientLogoSlotOf(req.Msg.Variant)}))
}

// clientLogoSlotOf maps a theme asset variant; an unspecified variant is the
// light slot, and an unknown one no slot, which the use case rejects.
func clientLogoSlotOf(variant managev1.ThemeAssetVariant) ClientLogoSlot {
	switch variant {
	case managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_UNSPECIFIED, managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT:
		return ClientLogoLight
	case managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK:
		return ClientLogoDark
	default:
		return ""
	}
}

func clientMessage(view ClientView) *managev1.Client {
	c := view.Client
	client := &managev1.Client{
		Id:        c.ID,
		Name:      c.Name,
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
	if c.Website != nil {
		client.Website = c.Website
	}
	client.LogoLightAsset = mediaasset.ReferenceProto(view.LightLogo)
	client.LogoDarkAsset = mediaasset.ReferenceProto(view.DarkLogo)
	return client
}

func clientWithStatsMessage(client ClientWorkCount) *managev1.ClientWithStats {
	return &managev1.ClientWithStats{Client: clientMessage(client.Client), WorkCount: client.WorkCount}
}
