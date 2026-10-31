package public

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
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
	return respond(&openv1.GetClientResponse{Client: client}, err)
}

func (h *ClientConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListClientsRequest]) (*connect.Response[openv1.ListClientsResponse], error) {
	page, err := h.catalog.ListClients(ctx, ListClientsQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&openv1.ListClientsResponse{Clients: page.Items, Pagination: page.Pagination}, err)
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
	return respond(&openv1.ListCategoriesResponse{Categories: page.Items, Pagination: page.Pagination}, err)
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
	return respond(&openv1.ListTagsResponse{Tags: page.Items, Pagination: page.Pagination}, err)
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
	return respond(&openv1.GetMapPlacesByIdsResponse{Places: places}, err)
}
