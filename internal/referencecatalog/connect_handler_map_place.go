package referencecatalog

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

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
	place, err := h.catalog.GetMapPlace(ctx, GetMapPlaceQuery{ID: req.Msg.Id})
	return respond(MapPlaceMessage(place), err)
}

func (h *MapPlaceConnectHandler) GetMapPlacesByIds(ctx context.Context, req *connect.Request[managev1.GetMapPlacesByIdsRequest]) (*connect.Response[managev1.GetMapPlacesByIdsResponse], error) {
	places, err := h.catalog.GetMapPlacesByIDs(ctx, GetMapPlacesByIDsQuery{IDs: req.Msg.Ids})
	return respond(&managev1.GetMapPlacesByIdsResponse{Places: messages(places, MapPlaceMessage)}, err)
}

func (h *MapPlaceConnectHandler) SearchMapPlaces(ctx context.Context, req *connect.Request[managev1.SearchMapPlacesRequest]) (*connect.Response[managev1.SearchMapPlacesResponse], error) {
	places, err := h.catalog.SearchMapPlaces(ctx, SearchMapPlacesQuery{Query: req.Msg.Query, Limit: req.Msg.Limit})
	return respond(&managev1.SearchMapPlacesResponse{Places: messages(places, mapPlaceBasicMessage)}, err)
}

func (h *MapPlaceConnectHandler) ListMapPlacesAdmin(ctx context.Context, req *connect.Request[managev1.ListMapPlacesAdminRequest]) (*connect.Response[managev1.ListMapPlacesAdminResponse], error) {
	page, err := h.catalog.ListMapPlacesAdmin(ctx, ListMapPlacesAdminQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&managev1.ListMapPlacesAdminResponse{Places: messages(page.Items, MapPlaceMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *MapPlaceConnectHandler) CreateMapPlace(ctx context.Context, req *connect.Request[managev1.CreateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	place, err := h.catalog.CreateMapPlace(ctx, CreateMapPlaceCommand{
		Name: req.Msg.Name, Address: req.Msg.Address, Lat: req.Msg.Lat, Lng: req.Msg.Lng,
		AddressComponents: AddressComponentsOf(req.Msg.AddressComponents), ImageFileID: req.Msg.ImageFileId,
		GooglePlaceID: req.Msg.GooglePlaceId,
	})
	return respond(MapPlaceMessage(place), err)
}

func (h *MapPlaceConnectHandler) UpdateMapPlace(ctx context.Context, req *connect.Request[managev1.UpdateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	place, err := h.catalog.UpdateMapPlace(ctx, UpdateMapPlaceCommand{
		ID: req.Msg.Id, Name: req.Msg.Name, Address: req.Msg.Address, Lat: req.Msg.Lat, Lng: req.Msg.Lng,
		AddressComponents: AddressComponentsOf(req.Msg.AddressComponents), ImageFileID: req.Msg.ImageFileId,
		ClearImage: req.Msg.ClearImage, GooglePlaceID: req.Msg.GooglePlaceId,
	})
	return respond(MapPlaceMessage(place), err)
}

func (h *MapPlaceConnectHandler) DeleteMapPlace(ctx context.Context, req *connect.Request[managev1.DeleteMapPlaceRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteMapPlace(ctx, DeleteMapPlaceCommand{ID: req.Msg.Id}))
}

// AddressComponentsOf converts the address components of a Map Place request;
// nil stays nil.
func AddressComponentsOf(components *managev1.AddressComponents) *AddressComponents {
	if components == nil {
		return nil
	}
	return &AddressComponents{
		Street: components.Street, City: components.City, Region: components.Region,
		Country: components.Country, PostalCode: components.PostalCode,
	}
}

// MapPlaceMessage renders a Map Place for the manage API and its MCP tools.
func MapPlaceMessage(view MapPlaceView) *managev1.MapPlace {
	p := view.Place
	proto := &managev1.MapPlace{
		Id:        p.ID,
		Name:      p.Name,
		Address:   p.Address,
		Lat:       p.Lat,
		Lng:       p.Lng,
		CreatedAt: timestamppb.New(p.CreatedAt),
		UpdatedAt: timestamppb.New(p.UpdatedAt),
	}

	if p.AddressComponents != nil {
		proto.AddressComponents = &managev1.AddressComponents{
			Street:     p.AddressComponents.Street,
			City:       p.AddressComponents.City,
			Region:     p.AddressComponents.Region,
			Country:    p.AddressComponents.Country,
			PostalCode: p.AddressComponents.PostalCode,
		}
	}

	if p.ImageFileID != nil && *p.ImageFileID != "" {
		proto.ImageFileId = p.ImageFileID
	}
	if p.GooglePlaceID != nil && *p.GooglePlaceID != "" {
		proto.GooglePlaceId = p.GooglePlaceID
	}
	if p.CreatedByMemberID != nil && *p.CreatedByMemberID != "" {
		proto.CreatedByMemberId = p.CreatedByMemberID
	}
	if p.UpdatedByMemberID != nil && *p.UpdatedByMemberID != "" {
		proto.UpdatedByMemberId = p.UpdatedByMemberID
	}
	proto.ImageAsset = mediaasset.ReferenceProto(view.ImageAsset)
	proto.CreatedByMember = memberSummaryMessage(view.CreatedByMember)
	proto.UpdatedByMember = memberSummaryMessage(view.UpdatedByMember)
	return proto
}

func memberSummaryMessage(member *MemberSummary) *commonv1.MemberSummary {
	if member == nil {
		return nil
	}
	return &commonv1.MemberSummary{Id: member.ID, Nickname: member.Nickname, AvatarAsset: memberref.AssetMessage(member.Avatar), Deleted: member.Deleted}
}

func mapPlaceBasicMessage(p MapPlace) *managev1.MapPlaceBasic {
	basic := &managev1.MapPlaceBasic{
		Id:      p.ID,
		Name:    p.Name,
		Address: p.Address,
		Lat:     p.Lat,
		Lng:     p.Lng,
	}
	if p.GooglePlaceID != nil && *p.GooglePlaceID != "" {
		basic.GooglePlaceId = p.GooglePlaceID
	}
	return basic
}
