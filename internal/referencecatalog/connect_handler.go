package referencecatalog

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

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

// PaginationMessage renders a page position, for the manage API and its MCP
// tools.
func PaginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

// messages maps items with message.
func messages[T, M any](items []T, message func(T) M) []M {
	result := make([]M, len(items))
	for i := range items {
		result[i] = message(items[i])
	}
	return result
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
	return respond(&managev1.ListGenresResponse{Genres: messages(page.Items, genreMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *GenreConnectHandler) ListGenresAdmin(ctx context.Context, req *connect.Request[managev1.ListGenresAdminRequest]) (*connect.Response[managev1.ListGenresAdminResponse], error) {
	page, err := h.catalog.ListGenresAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListGenresAdminResponse{Genres: messages(page.Items, genreWithStatsMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *GenreConnectHandler) CreateGenre(ctx context.Context, req *connect.Request[managev1.CreateGenreRequest]) (*connect.Response[managev1.Genre], error) {
	reference, err := h.catalog.CreateGenre(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description})
	return respond(genreMessage(reference), err)
}

func (h *GenreConnectHandler) UpdateGenre(ctx context.Context, req *connect.Request[managev1.UpdateGenreRequest]) (*connect.Response[managev1.Genre], error) {
	reference, err := h.catalog.UpdateGenre(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description})
	return respond(genreMessage(reference), err)
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
	return respond(&managev1.ListStylesResponse{Styles: messages(page.Items, styleMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *StyleConnectHandler) ListStylesAdmin(ctx context.Context, req *connect.Request[managev1.ListStylesAdminRequest]) (*connect.Response[managev1.ListStylesAdminResponse], error) {
	page, err := h.catalog.ListStylesAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListStylesAdminResponse{Styles: messages(page.Items, styleWithStatsMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *StyleConnectHandler) CreateStyle(ctx context.Context, req *connect.Request[managev1.CreateStyleRequest]) (*connect.Response[managev1.Style], error) {
	reference, err := h.catalog.CreateStyle(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description})
	return respond(styleMessage(reference), err)
}

func (h *StyleConnectHandler) UpdateStyle(ctx context.Context, req *connect.Request[managev1.UpdateStyleRequest]) (*connect.Response[managev1.Style], error) {
	reference, err := h.catalog.UpdateStyle(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description})
	return respond(styleMessage(reference), err)
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
	return respond(&managev1.ListFormatsResponse{Formats: messages(page.Items, formatMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *FormatConnectHandler) ListFormatsAdmin(ctx context.Context, req *connect.Request[managev1.ListFormatsAdminRequest]) (*connect.Response[managev1.ListFormatsAdminResponse], error) {
	page, err := h.catalog.ListFormatsAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListFormatsAdminResponse{Formats: messages(page.Items, formatWithStatsMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *FormatConnectHandler) CreateFormat(ctx context.Context, req *connect.Request[managev1.CreateFormatRequest]) (*connect.Response[managev1.Format], error) {
	reference, err := h.catalog.CreateFormat(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug})
	return respond(formatMessage(reference), err)
}

func (h *FormatConnectHandler) UpdateFormat(ctx context.Context, req *connect.Request[managev1.UpdateFormatRequest]) (*connect.Response[managev1.Format], error) {
	reference, err := h.catalog.UpdateFormat(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug})
	return respond(formatMessage(reference), err)
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
	return respond(&managev1.ListTagsResponse{Tags: messages(page.Items, tagMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *TagConnectHandler) ListTagsAdmin(ctx context.Context, req *connect.Request[managev1.ListTagsAdminRequest]) (*connect.Response[managev1.ListTagsAdminResponse], error) {
	page, err := h.catalog.ListTagsAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListTagsAdminResponse{Tags: messages(page.Items, tagWithStatsMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *TagConnectHandler) CreateTag(ctx context.Context, req *connect.Request[managev1.CreateTagRequest]) (*connect.Response[managev1.Tag], error) {
	reference, err := h.catalog.CreateTag(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug})
	return respond(tagMessage(reference), err)
}

func (h *TagConnectHandler) UpdateTag(ctx context.Context, req *connect.Request[managev1.UpdateTagRequest]) (*connect.Response[managev1.Tag], error) {
	reference, err := h.catalog.UpdateTag(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug})
	return respond(tagMessage(reference), err)
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
	return respond(&managev1.ListCategoriesResponse{Categories: messages(page.Items, categoryMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *CategoryConnectHandler) ListCategoriesAdmin(ctx context.Context, req *connect.Request[managev1.ListCategoriesAdminRequest]) (*connect.Response[managev1.ListCategoriesAdminResponse], error) {
	page, err := h.catalog.ListCategoriesAdmin(ctx, referenceListQuery(req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination))
	return respond(&managev1.ListCategoriesAdminResponse{Categories: messages(page.Items, categoryWithStatsMessage), Pagination: PaginationMessage(page.Pagination)}, err)
}

func (h *CategoryConnectHandler) CreateCategory(ctx context.Context, req *connect.Request[managev1.CreateCategoryRequest]) (*connect.Response[managev1.Category], error) {
	reference, err := h.catalog.CreateCategory(ctx, CreateReferenceCommand{Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description})
	return respond(categoryMessage(reference), err)
}

func (h *CategoryConnectHandler) UpdateCategory(ctx context.Context, req *connect.Request[managev1.UpdateCategoryRequest]) (*connect.Response[managev1.Category], error) {
	reference, err := h.catalog.UpdateCategory(ctx, UpdateReferenceCommand{ID: req.Msg.Id, Name: req.Msg.Name, Slug: req.Msg.Slug, Description: req.Msg.Description})
	return respond(categoryMessage(reference), err)
}

func (h *CategoryConnectHandler) DeleteCategory(ctx context.Context, req *connect.Request[managev1.DeleteCategoryRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.catalog.DeleteCategory(ctx, DeleteReferenceCommand{ID: req.Msg.Id}))
}

func categoryMessage(c Reference) *managev1.Category {
	category := &managev1.Category{
		Id:          c.ID,
		Name:        c.Name,
		Slug:        &c.Slug,
		Description: c.Description,
		CreatedAt:   timestamppb.New(c.CreatedAt),
	}
	if c.UpdatedAt != nil {
		category.UpdatedAt = timestamppb.New(*c.UpdatedAt)
	}
	return category
}

// categoryWithStatsMessage reports the usage as PostCount, which predates
// Release categories and counts both owners.
func categoryWithStatsMessage(usage ReferenceUsage) *managev1.CategoryWithStats {
	return &managev1.CategoryWithStats{Category: categoryMessage(usage.Reference), PostCount: usage.Count}
}

func genreMessage(genre Reference) *managev1.Genre {
	return &managev1.Genre{
		Id: genre.ID, Name: genre.Name, Slug: genre.Slug, Description: genre.Description, CreatedAt: timestamppb.New(genre.CreatedAt),
	}
}

func genreWithStatsMessage(usage ReferenceUsage) *managev1.GenreWithStats {
	return &managev1.GenreWithStats{Genre: genreMessage(usage.Reference), ReleaseCount: usage.Count}
}

func styleMessage(style Reference) *managev1.Style {
	return &managev1.Style{
		Id: style.ID, Name: style.Name, Slug: style.Slug, Description: style.Description, CreatedAt: timestamppb.New(style.CreatedAt),
	}
}

func styleWithStatsMessage(usage ReferenceUsage) *managev1.StyleWithStats {
	return &managev1.StyleWithStats{Style: styleMessage(usage.Reference), ReleaseCount: usage.Count}
}

func formatMessage(f Reference) *managev1.Format {
	return &managev1.Format{
		Id:   f.ID,
		Name: f.Name,
		Slug: f.Slug,
	}
}

func formatWithStatsMessage(usage ReferenceUsage) *managev1.FormatWithStats {
	return &managev1.FormatWithStats{Format: formatMessage(usage.Reference), ReleaseCount: usage.Count}
}

// tagMessage maps a tag; the tag table has no updated_at column.
func tagMessage(t Reference) *managev1.Tag {
	return &managev1.Tag{
		Id:        t.ID,
		Name:      t.Name,
		Slug:      &t.Slug,
		CreatedAt: timestamppb.New(t.CreatedAt),
	}
}

func tagWithStatsMessage(usage ReferenceUsage) *managev1.TagWithStats {
	return &managev1.TagWithStats{Tag: tagMessage(usage.Reference), PostCount: usage.Count}
}
