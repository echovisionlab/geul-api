package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// ClientConnectHandler serves the public Client RPCs.
type ClientConnectHandler struct {
	openv1connect.UnimplementedClientServiceHandler
	catalog *Service
}

// NewClientConnectHandler returns the public Client Connect adapter of catalog.
func NewClientConnectHandler(catalog *Service) *ClientConnectHandler {
	return &ClientConnectHandler{catalog: catalog}
}

func (h *ClientConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetClientRequest]) (*connect.Response[openv1.GetClientResponse], error) {
	client, err := h.catalog.GetClient(ctx, GetClientQuery{ID: req.Msg.Id})
	return respond(&openv1.GetClientResponse{Client: clientSummaryMessage(client)}, err)
}

func (h *ClientConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListClientsRequest]) (*connect.Response[openv1.ListClientsResponse], error) {
	page, err := h.catalog.ListClients(ctx, ListClientsQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&openv1.ListClientsResponse{Clients: messages(page.Items, clientSummaryMessage), Pagination: paginationMessage(page.Pagination)}, err)
}

// CategoryConnectHandler serves the public Category RPCs.
type CategoryConnectHandler struct {
	openv1connect.UnimplementedCategoryServiceHandler
	catalog *Service
}

// NewCategoryConnectHandler returns the public Category Connect adapter of catalog.
func NewCategoryConnectHandler(catalog *Service) *CategoryConnectHandler {
	return &CategoryConnectHandler{catalog: catalog}
}

func (h *CategoryConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListCategoriesRequest]) (*connect.Response[openv1.ListCategoriesResponse], error) {
	page, err := h.catalog.ListCategories(ctx, ListCategoriesQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&openv1.ListCategoriesResponse{Categories: messages(page.Items, categoryMessage), Pagination: paginationMessage(page.Pagination)}, err)
}

// TagConnectHandler serves the public Tag RPCs.
type TagConnectHandler struct {
	openv1connect.UnimplementedTagServiceHandler
	catalog *Service
}

// NewTagConnectHandler returns the public Tag Connect adapter of catalog.
func NewTagConnectHandler(catalog *Service) *TagConnectHandler {
	return &TagConnectHandler{catalog: catalog}
}

func (h *TagConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListTagsRequest]) (*connect.Response[openv1.ListTagsResponse], error) {
	page, err := h.catalog.ListTags(ctx, ListTagsQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&openv1.ListTagsResponse{Tags: messages(page.Items, tagMessage), Pagination: paginationMessage(page.Pagination)}, err)
}

// MapPlaceConnectHandler serves the public Map Place RPCs.
type MapPlaceConnectHandler struct {
	openv1connect.UnimplementedMapPlaceServiceHandler
	catalog *Service
}

// NewMapPlaceConnectHandler returns the public Map Place Connect adapter of catalog.
func NewMapPlaceConnectHandler(catalog *Service) *MapPlaceConnectHandler {
	return &MapPlaceConnectHandler{catalog: catalog}
}

func (h *MapPlaceConnectHandler) GetByIds(ctx context.Context, req *connect.Request[openv1.GetMapPlacesByIdsRequest]) (*connect.Response[openv1.GetMapPlacesByIdsResponse], error) {
	places, err := h.catalog.GetMapPlacesByIDs(ctx, GetMapPlacesByIDsQuery{IDs: req.Msg.Ids})
	return respond(&openv1.GetMapPlacesByIdsResponse{Places: messages(places, mapPlaceMessage)}, err)
}

func messages[T, M any](items []T, message func(T) M) []M {
	result := make([]M, 0, len(items))
	for _, item := range items {
		result = append(result, message(item))
	}
	return result
}

func paginationMessage(pagination referencecatalog.Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

func categoryMessage(category Category) *openv1.Category {
	return &openv1.Category{
		Id:          category.ID,
		Name:        category.Name,
		Slug:        category.Slug,
		Description: category.Description,
		PostCount:   category.PostCount,
	}
}

func tagMessage(tag Tag) *openv1.Tag {
	return &openv1.Tag{
		Id:        tag.ID,
		Name:      tag.Name,
		Slug:      tag.Slug,
		PostCount: tag.PostCount,
	}
}

func clientSummaryMessage(view referencecatalog.ClientView) *openv1.ClientSummary {
	client := view.Client
	summary := &openv1.ClientSummary{
		Id:        client.ID,
		Name:      client.Name,
		CreatedAt: timestamppb.New(client.CreatedAt),
	}
	if client.Website != nil {
		summary.Website = client.Website
	}
	summary.LogoLightAsset = mediaasset.ReferenceProto(view.LightLogo)
	summary.LogoDarkAsset = mediaasset.ReferenceProto(view.DarkLogo)
	return summary
}

func mapPlaceMessage(public MapPlace) *openv1.MapPlace {
	place := public.Place
	protoPlace := &openv1.MapPlace{
		Id:        place.ID,
		Name:      place.Name,
		Address:   place.Address,
		Lat:       place.Lat,
		Lng:       place.Lng,
		CreatedAt: timestamppb.New(place.CreatedAt),
	}

	if place.AddressComponents != nil {
		protoPlace.AddressComponents = &openv1.AddressComponents{
			Street:     place.AddressComponents.Street,
			City:       place.AddressComponents.City,
			Region:     place.AddressComponents.Region,
			Country:    place.AddressComponents.Country,
			PostalCode: place.AddressComponents.PostalCode,
		}
	}

	if place.GooglePlaceID != nil && *place.GooglePlaceID != "" {
		protoPlace.GooglePlaceId = place.GooglePlaceID
	}
	protoPlace.ImageAsset = mediaasset.ReferenceProto(public.Image)
	return protoPlace
}
