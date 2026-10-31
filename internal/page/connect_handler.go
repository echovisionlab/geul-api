package page

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the Page management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedPageServiceHandler
	pages *Service
}

// NewConnectHandler returns the Connect adapter of pages.
func NewConnectHandler(pages *Service) *ConnectHandler {
	return &ConnectHandler{pages: pages}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) GetPage(ctx context.Context, req *connect.Request[managev1.GetPageRequest]) (*connect.Response[managev1.Page], error) {
	return respond(h.pages.GetPage(ctx, GetPageQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) GetPageBySlug(ctx context.Context, req *connect.Request[managev1.GetPageBySlugRequest]) (*connect.Response[managev1.Page], error) {
	return respond(h.pages.GetPageBySlug(ctx, GetPageBySlugQuery{Slug: req.Msg.Slug}))
}

func (h *ConnectHandler) GetPageEditorDocument(ctx context.Context, req *connect.Request[managev1.GetPageEditorDocumentRequest]) (*connect.Response[managev1.GetPageEditorDocumentResponse], error) {
	return respond(h.pages.GetPageEditorDocument(ctx, GetPageEditorDocumentQuery{PageID: req.Msg.PageId, Locale: req.Msg.Locale}))
}

func (h *ConnectHandler) ListPagesAdmin(ctx context.Context, req *connect.Request[managev1.ListPagesAdminRequest]) (*connect.Response[managev1.ListPagesAdminResponse], error) {
	pagination := req.Msg.GetPagination()
	return respond(h.pages.ListPagesAdmin(ctx, ListPagesAdminQuery{
		Page: Page{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()}, Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	}))
}

func (h *ConnectHandler) CreatePage(ctx context.Context, req *connect.Request[managev1.CreatePageRequest]) (*connect.Response[managev1.Page], error) {
	return respond(h.pages.CreatePage(ctx, CreatePageCommand{
		Title: req.Msg.Title, Slug: req.Msg.Slug, Summary: req.Msg.Summary, ShowTitle: req.Msg.ShowTitle,
		SourceLocale: req.Msg.SourceLocale, AccessPolicy: req.Msg.AccessPolicy, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) UpdatePage(ctx context.Context, req *connect.Request[managev1.UpdatePageRequest]) (*connect.Response[managev1.UpdatePageResponse], error) {
	return respond(h.pages.UpdatePage(ctx, UpdatePageCommand{
		ID: req.Msg.Id, Slug: req.Msg.Slug, ShowTitle: req.Msg.ShowTitle, AccessPolicy: req.Msg.AccessPolicy,
	}))
}

func (h *ConnectHandler) DeletePage(ctx context.Context, req *connect.Request[managev1.DeletePageRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, h.pages.DeletePage(ctx, DeletePageCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishPage(ctx context.Context, req *connect.Request[managev1.PublishPageRequest]) (*connect.Response[managev1.PageLifecycleMutationResponse], error) {
	return respond(h.pages.PublishPage(ctx, PublishPageCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) UnpublishPage(ctx context.Context, req *connect.Request[managev1.UnpublishPageRequest]) (*connect.Response[managev1.PageLifecycleMutationResponse], error) {
	return respond(h.pages.UnpublishPage(ctx, UnpublishPageCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) SetPageFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetPageFeaturedImageRequest]) (*connect.Response[managev1.SetPageFeaturedImageResponse], error) {
	return respond(h.pages.SetPageFeaturedImage(ctx, SetPageFeaturedImageCommand{PageID: req.Msg.PageId, FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) DeletePageFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeletePageFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	return respond(h.pages.DeletePageFeaturedImage(ctx, DeletePageFeaturedImageCommand{PageID: req.Msg.PageId}))
}

func (h *ConnectHandler) CheckPageSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckPageSlugAvailableRequest]) (*connect.Response[managev1.CheckPageSlugAvailableResponse], error) {
	return respond(h.pages.CheckPageSlugAvailable(ctx, CheckPageSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeID: req.Msg.ExcludeId}))
}

func (h *ConnectHandler) ListPageVersions(ctx context.Context, req *connect.Request[managev1.ListPageVersionsRequest]) (*connect.Response[managev1.ListPageVersionsResponse], error) {
	pagination := req.Msg.GetPagination()
	return respond(h.pages.ListPageVersions(ctx, ListPageVersionsQuery{
		PageID: req.Msg.PageId, Page: Page{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()},
	}))
}

func (h *ConnectHandler) RestorePageVersion(ctx context.Context, req *connect.Request[managev1.RestorePageVersionRequest]) (*connect.Response[managev1.Page], error) {
	return respond(h.pages.RestorePageVersion(ctx, RestorePageVersionCommand{PageID: req.Msg.PageId, VersionID: req.Msg.VersionId}))
}

// InternalConnectHandler serves the Page collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalPageServiceHandler
	pages *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of pages.
func NewInternalConnectHandler(pages *Service) *InternalConnectHandler {
	return &InternalConnectHandler{pages: pages}
}

func (h *InternalConnectHandler) LoadPageBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadPageBlockDocumentRequest]) (*connect.Response[intrav1.LoadPageBlockDocumentResponse], error) {
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	return respond(h.pages.LoadPageBlockDocument(ctx, LoadPageBlockDocumentQuery{
		PageID: req.Msg.PageId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	}))
}

func (h *InternalConnectHandler) ApplyPageBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyPageBlockBatchRequest]) (*connect.Response[intrav1.ApplyPageBlockBatchResponse], error) {
	return respond(h.pages.ApplyPageBlockBatch(ctx, ApplyPageBlockBatchCommand{
		PageID: req.Msg.GetPageId(), Locale: req.Msg.GetLocale(), Batch: req.Msg.GetBatch(),
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, AffectedLocaleValues: req.Msg.GetAffectedLocaleValues(),
	}))
}

func (h *InternalConnectHandler) UpdatePageLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdatePageLocaleMetadataRequest]) (*connect.Response[intrav1.UpdatePageLocaleMetadataResponse], error) {
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	command := UpdatePageLocaleMetadataCommand{
		PageID: req.Msg.PageId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, Title: req.Msg.Title,
		ContributorMemberIDs: req.Msg.ContributorMemberIds,
	}
	switch change := req.Msg.SummaryChange.(type) {
	case *intrav1.UpdatePageLocaleMetadataRequest_SetSummary:
		command.SetSummary = &change.SetSummary
	case *intrav1.UpdatePageLocaleMetadataRequest_ClearSummary:
		command.ClearSummary = true
	}
	return respond(h.pages.UpdatePageLocaleMetadata(ctx, command))
}

func (h *InternalConnectHandler) UpdatePageDocumentMetadata(ctx context.Context, req *connect.Request[intrav1.UpdatePageDocumentMetadataRequest]) (*connect.Response[intrav1.UpdatePageDocumentMetadataResponse], error) {
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	return respond(h.pages.UpdatePageDocumentMetadata(ctx, UpdatePageDocumentMetadataCommand{
		PageID: req.Msg.PageId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		DocumentLayout: req.Msg.DocumentLayout, ContributorMemberIDs: req.Msg.ContributorMemberIds,
	}))
}

func (h *InternalConnectHandler) CreatePageVersionCheckpoint(ctx context.Context, req *connect.Request[intrav1.CreatePageVersionCheckpointRequest]) (*connect.Response[intrav1.CreatePageVersionCheckpointResponse], error) {
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	return respond(h.pages.CreatePageVersionCheckpoint(ctx, CreatePageVersionCheckpointCommand{
		PageID: req.Msg.PageId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		ContributorMemberIDs: req.Msg.ContributorMemberIds,
	}))
}
