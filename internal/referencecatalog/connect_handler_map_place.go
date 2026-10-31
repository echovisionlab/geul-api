package referencecatalog

import (
	"context"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// MapPlaceConnectHandler serves the Map Place RPCs.
type MapPlaceConnectHandler struct {
	managev1connect.UnimplementedMapPlaceServiceHandler
	catalog *Service
}

// NewMapPlaceConnectHandler returns the Map Place Connect adapter of catalog.
func NewMapPlaceConnectHandler(catalog *Service) *MapPlaceConnectHandler {
	return &MapPlaceConnectHandler{catalog: catalog}
}

func (h *MapPlaceConnectHandler) GetMapPlace(ctx context.Context, req *connect.Request[managev1.GetMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	return respond(h.catalog.GetMapPlace(ctx, GetMapPlaceQuery{ID: req.Msg.Id}))
}

func (h *MapPlaceConnectHandler) GetMapPlacesByIds(ctx context.Context, req *connect.Request[managev1.GetMapPlacesByIdsRequest]) (*connect.Response[managev1.GetMapPlacesByIdsResponse], error) {
	places, err := h.catalog.GetMapPlacesByIDs(ctx, GetMapPlacesByIDsQuery{IDs: req.Msg.Ids})
	return respond(&managev1.GetMapPlacesByIdsResponse{Places: places}, err)
}

func (h *MapPlaceConnectHandler) SearchMapPlaces(ctx context.Context, req *connect.Request[managev1.SearchMapPlacesRequest]) (*connect.Response[managev1.SearchMapPlacesResponse], error) {
	places, err := h.catalog.SearchMapPlaces(ctx, SearchMapPlacesQuery{Query: req.Msg.Query, Limit: req.Msg.Limit})
	return respond(&managev1.SearchMapPlacesResponse{Places: places}, err)
}

func (h *MapPlaceConnectHandler) ListMapPlacesAdmin(ctx context.Context, req *connect.Request[managev1.ListMapPlacesAdminRequest]) (*connect.Response[managev1.ListMapPlacesAdminResponse], error) {
	page, err := h.catalog.ListMapPlacesAdmin(ctx, ListMapPlacesAdminQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&managev1.ListMapPlacesAdminResponse{Places: page.Items, Pagination: page.Pagination}, err)
}

func (h *MapPlaceConnectHandler) CreateMapPlace(ctx context.Context, req *connect.Request[managev1.CreateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	return respond(h.catalog.CreateMapPlace(ctx, CreateMapPlaceCommand{
		Name: req.Msg.Name, Address: req.Msg.Address, Lat: req.Msg.Lat, Lng: req.Msg.Lng,
		AddressComponents: AddressComponentsOf(req.Msg.AddressComponents), ImageFileID: req.Msg.ImageFileId,
		GooglePlaceID: req.Msg.GooglePlaceId,
	}))
}

func (h *MapPlaceConnectHandler) UpdateMapPlace(ctx context.Context, req *connect.Request[managev1.UpdateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	return respond(h.catalog.UpdateMapPlace(ctx, UpdateMapPlaceCommand{
		ID: req.Msg.Id, Name: req.Msg.Name, Address: req.Msg.Address, Lat: req.Msg.Lat, Lng: req.Msg.Lng,
		AddressComponents: AddressComponentsOf(req.Msg.AddressComponents), ImageFileID: req.Msg.ImageFileId,
		ClearImage: req.Msg.ClearImage, GooglePlaceID: req.Msg.GooglePlaceId,
	}))
}

func (h *MapPlaceConnectHandler) DeleteMapPlace(ctx context.Context, req *connect.Request[managev1.DeleteMapPlaceRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteMapPlace(ctx, DeleteMapPlaceCommand{ID: req.Msg.Id}))
}

// AddressComponentsOf converts the address components of a Map Place request;
// nil stays nil.
func AddressComponentsOf(components *managev1.AddressComponents) *model.AddressComponents {
	if components == nil {
		return nil
	}
	return &model.AddressComponents{
		Street: components.Street, City: components.City, Region: components.Region,
		Country: components.Country, PostalCode: components.PostalCode,
	}
}
