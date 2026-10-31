package referencecatalog

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// Each Connect handler serves one administrator catalog RPC service. A method
// translates the request into a use-case command or query, and the result
// into the response; errs.Wrap maps use-case errors to Connect codes.

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func deleted(err error) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func referenceListQuery(filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, pagination *commonv1.PaginationRequest) ReferenceListQuery {
	return ReferenceListQuery{Filters: filters, Sorts: sorts, Limit: pagination.GetLimit(), Offset: pagination.GetOffset()}
}

// GenreConnectHandler serves the Genre RPCs.
type GenreConnectHandler struct {
	managev1connect.UnimplementedGenreServiceHandler
	catalog *Service
}

// NewGenreConnectHandler returns the Genre Connect adapter of catalog.
func NewGenreConnectHandler(catalog *Service) *GenreConnectHandler {
	return &GenreConnectHandler{catalog: catalog}
}

func (h *GenreConnectHandler) ListGenres(ctx context.Context, req *connect.Request[managev1.ListGenresRequest]) (*connect.Response[managev1.ListGenresResponse], error) {
	page, err := h.catalog.ListGenres(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListGenresResponse{Genres: page.Items, Pagination: page.Pagination}, err)
}

func (h *GenreConnectHandler) ListGenresAdmin(ctx context.Context, req *connect.Request[managev1.ListGenresAdminRequest]) (*connect.Response[managev1.ListGenresAdminResponse], error) {
	page, err := h.catalog.ListGenresAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListGenresAdminResponse{Genres: page.Items, Pagination: page.Pagination}, err)
}

func (h *GenreConnectHandler) CreateGenre(ctx context.Context, req *connect.Request[managev1.CreateGenreRequest]) (*connect.Response[managev1.Genre], error) {
	return respond(h.catalog.CreateGenre(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description}))
}

func (h *GenreConnectHandler) UpdateGenre(ctx context.Context, req *connect.Request[managev1.UpdateGenreRequest]) (*connect.Response[managev1.Genre], error) {
	return respond(h.catalog.UpdateGenre(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description}))
}

func (h *GenreConnectHandler) DeleteGenre(ctx context.Context, req *connect.Request[managev1.DeleteGenreRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteGenre(ctx, DeleteReferenceCommand{ID: req.Msg.Id}))
}

// StyleConnectHandler serves the Style RPCs.
type StyleConnectHandler struct {
	managev1connect.UnimplementedStyleServiceHandler
	catalog *Service
}

// NewStyleConnectHandler returns the Style Connect adapter of catalog.
func NewStyleConnectHandler(catalog *Service) *StyleConnectHandler {
	return &StyleConnectHandler{catalog: catalog}
}

func (h *StyleConnectHandler) ListStyles(ctx context.Context, req *connect.Request[managev1.ListStylesRequest]) (*connect.Response[managev1.ListStylesResponse], error) {
	page, err := h.catalog.ListStyles(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListStylesResponse{Styles: page.Items, Pagination: page.Pagination}, err)
}

func (h *StyleConnectHandler) ListStylesAdmin(ctx context.Context, req *connect.Request[managev1.ListStylesAdminRequest]) (*connect.Response[managev1.ListStylesAdminResponse], error) {
	page, err := h.catalog.ListStylesAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListStylesAdminResponse{Styles: page.Items, Pagination: page.Pagination}, err)
}

func (h *StyleConnectHandler) CreateStyle(ctx context.Context, req *connect.Request[managev1.CreateStyleRequest]) (*connect.Response[managev1.Style], error) {
	return respond(h.catalog.CreateStyle(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description}))
}

func (h *StyleConnectHandler) UpdateStyle(ctx context.Context, req *connect.Request[managev1.UpdateStyleRequest]) (*connect.Response[managev1.Style], error) {
	return respond(h.catalog.UpdateStyle(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description}))
}

func (h *StyleConnectHandler) DeleteStyle(ctx context.Context, req *connect.Request[managev1.DeleteStyleRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteStyle(ctx, DeleteReferenceCommand{ID: req.Msg.Id}))
}

// FormatConnectHandler serves the Format RPCs.
type FormatConnectHandler struct {
	managev1connect.UnimplementedFormatServiceHandler
	catalog *Service
}

// NewFormatConnectHandler returns the Format Connect adapter of catalog.
func NewFormatConnectHandler(catalog *Service) *FormatConnectHandler {
	return &FormatConnectHandler{catalog: catalog}
}

func (h *FormatConnectHandler) ListFormats(ctx context.Context, req *connect.Request[managev1.ListFormatsRequest]) (*connect.Response[managev1.ListFormatsResponse], error) {
	page, err := h.catalog.ListFormats(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListFormatsResponse{Formats: page.Items, Pagination: page.Pagination}, err)
}

func (h *FormatConnectHandler) ListFormatsAdmin(ctx context.Context, req *connect.Request[managev1.ListFormatsAdminRequest]) (*connect.Response[managev1.ListFormatsAdminResponse], error) {
	page, err := h.catalog.ListFormatsAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListFormatsAdminResponse{Formats: page.Items, Pagination: page.Pagination}, err)
}

func (h *FormatConnectHandler) CreateFormat(ctx context.Context, req *connect.Request[managev1.CreateFormatRequest]) (*connect.Response[managev1.Format], error) {
	return respond(h.catalog.CreateFormat(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug}))
}

func (h *FormatConnectHandler) UpdateFormat(ctx context.Context, req *connect.Request[managev1.UpdateFormatRequest]) (*connect.Response[managev1.Format], error) {
	return respond(h.catalog.UpdateFormat(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug}))
}

func (h *FormatConnectHandler) DeleteFormat(ctx context.Context, req *connect.Request[managev1.DeleteFormatRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteFormat(ctx, DeleteReferenceCommand{ID: req.Msg.Id}))
}

// TagConnectHandler serves the Tag RPCs.
type TagConnectHandler struct {
	managev1connect.UnimplementedTagServiceHandler
	catalog *Service
}

// NewTagConnectHandler returns the Tag Connect adapter of catalog.
func NewTagConnectHandler(catalog *Service) *TagConnectHandler {
	return &TagConnectHandler{catalog: catalog}
}

func (h *TagConnectHandler) ListTags(ctx context.Context, req *connect.Request[managev1.ListTagsRequest]) (*connect.Response[managev1.ListTagsResponse], error) {
	page, err := h.catalog.ListTags(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListTagsResponse{Tags: page.Items, Pagination: page.Pagination}, err)
}

func (h *TagConnectHandler) ListTagsAdmin(ctx context.Context, req *connect.Request[managev1.ListTagsAdminRequest]) (*connect.Response[managev1.ListTagsAdminResponse], error) {
	page, err := h.catalog.ListTagsAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListTagsAdminResponse{Tags: page.Items, Pagination: page.Pagination}, err)
}

func (h *TagConnectHandler) CreateTag(ctx context.Context, req *connect.Request[managev1.CreateTagRequest]) (*connect.Response[managev1.Tag], error) {
	return respond(h.catalog.CreateTag(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug}))
}

func (h *TagConnectHandler) UpdateTag(ctx context.Context, req *connect.Request[managev1.UpdateTagRequest]) (*connect.Response[managev1.Tag], error) {
	return respond(h.catalog.UpdateTag(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug}))
}

func (h *TagConnectHandler) DeleteTag(ctx context.Context, req *connect.Request[managev1.DeleteTagRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteTag(ctx, DeleteReferenceCommand{ID: req.Msg.Id}))
}

// CategoryConnectHandler serves the Category RPCs.
type CategoryConnectHandler struct {
	managev1connect.UnimplementedCategoryServiceHandler
	catalog *Service
}

// NewCategoryConnectHandler returns the Category Connect adapter of catalog.
func NewCategoryConnectHandler(catalog *Service) *CategoryConnectHandler {
	return &CategoryConnectHandler{catalog: catalog}
}

func (h *CategoryConnectHandler) ListCategories(ctx context.Context, req *connect.Request[managev1.ListCategoriesRequest]) (*connect.Response[managev1.ListCategoriesResponse], error) {
	page, err := h.catalog.ListCategories(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListCategoriesResponse{Categories: page.Items, Pagination: page.Pagination}, err)
}

func (h *CategoryConnectHandler) ListCategoriesAdmin(ctx context.Context, req *connect.Request[managev1.ListCategoriesAdminRequest]) (*connect.Response[managev1.ListCategoriesAdminResponse], error) {
	page, err := h.catalog.ListCategoriesAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListCategoriesAdminResponse{Categories: page.Items, Pagination: page.Pagination}, err)
}

func (h *CategoryConnectHandler) CreateCategory(ctx context.Context, req *connect.Request[managev1.CreateCategoryRequest]) (*connect.Response[managev1.Category], error) {
	return respond(h.catalog.CreateCategory(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description}))
}

func (h *CategoryConnectHandler) UpdateCategory(ctx context.Context, req *connect.Request[managev1.UpdateCategoryRequest]) (*connect.Response[managev1.Category], error) {
	return respond(h.catalog.UpdateCategory(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description}))
}

func (h *CategoryConnectHandler) DeleteCategory(ctx context.Context, req *connect.Request[managev1.DeleteCategoryRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteCategory(ctx, DeleteReferenceCommand{ID: req.Msg.Id}))
}
