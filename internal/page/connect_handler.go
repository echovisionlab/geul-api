package page

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// respondWith renders a use case's result through message.
func respondWith[R, T any](result R, err error, message func(R) (*T, error)) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	rendered, err := message(result)
	return respond(rendered, err)
}

func (h *ConnectHandler) GetPage(ctx context.Context, req *connect.Request[managev1.GetPageRequest]) (*connect.Response[managev1.Page], error) {
	detail, err := h.pages.GetPage(ctx, GetPageQuery{ID: req.Msg.Id})
	return respondWith(detail, err, PageMessage)
}

func (h *ConnectHandler) GetPageBySlug(ctx context.Context, req *connect.Request[managev1.GetPageBySlugRequest]) (*connect.Response[managev1.Page], error) {
	detail, err := h.pages.GetPageBySlug(ctx, GetPageBySlugQuery{Slug: req.Msg.Slug})
	return respondWith(detail, err, PageMessage)
}

func (h *ConnectHandler) GetPageEditorDocument(ctx context.Context, req *connect.Request[managev1.GetPageEditorDocumentRequest]) (*connect.Response[managev1.GetPageEditorDocumentResponse], error) {
	document, err := h.pages.GetPageEditorDocument(ctx, GetPageEditorDocumentQuery{PageID: req.Msg.PageId, Locale: req.Msg.Locale})
	return respondWith(document, err, editorDocumentMessage)
}

func (h *ConnectHandler) ListPagesAdmin(ctx context.Context, req *connect.Request[managev1.ListPagesAdminRequest]) (*connect.Response[managev1.ListPagesAdminResponse], error) {
	pagination := req.Msg.GetPagination()
	list, err := h.pages.ListPagesAdmin(ctx, ListPagesAdminQuery{
		Page: Paging{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()}, Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respondWith(list, err, PageListMessage)
}

func (h *ConnectHandler) CreatePage(ctx context.Context, req *connect.Request[managev1.CreatePageRequest]) (*connect.Response[managev1.Page], error) {
	detail, err := h.pages.CreatePage(ctx, CreatePageCommand{
		Title: req.Msg.Title, Slug: req.Msg.Slug, Summary: req.Msg.Summary, ShowTitle: req.Msg.ShowTitle,
		SourceLocale: req.Msg.SourceLocale, AccessPolicy: AccessPolicyInput(req.Msg.AccessPolicy),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respondWith(detail, err, PageMessage)
}

func (h *ConnectHandler) UpdatePage(ctx context.Context, req *connect.Request[managev1.UpdatePageRequest]) (*connect.Response[managev1.UpdatePageResponse], error) {
	updated, err := h.pages.UpdatePage(ctx, UpdatePageCommand{
		ID: req.Msg.Id, Slug: req.Msg.Slug, ShowTitle: req.Msg.ShowTitle, AccessPolicy: AccessPolicyInput(req.Msg.AccessPolicy),
	})
	return respondWith(updated, err, UpdateResponseMessage)
}

func (h *ConnectHandler) DeletePage(ctx context.Context, req *connect.Request[managev1.DeletePageRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, h.pages.DeletePage(ctx, DeletePageCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishPage(ctx context.Context, req *connect.Request[managev1.PublishPageRequest]) (*connect.Response[managev1.PageLifecycleMutationResponse], error) {
	result, err := h.pages.PublishPage(ctx, PublishPageCommand{ID: req.Msg.Id})
	return respondWith(result, err, LifecycleMessage)
}

func (h *ConnectHandler) UnpublishPage(ctx context.Context, req *connect.Request[managev1.UnpublishPageRequest]) (*connect.Response[managev1.PageLifecycleMutationResponse], error) {
	result, err := h.pages.UnpublishPage(ctx, UnpublishPageCommand{ID: req.Msg.Id})
	return respondWith(result, err, LifecycleMessage)
}

func (h *ConnectHandler) SetPageFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetPageFeaturedImageRequest]) (*connect.Response[managev1.SetPageFeaturedImageResponse], error) {
	result, err := h.pages.SetPageFeaturedImage(ctx, SetPageFeaturedImageCommand{PageID: req.Msg.PageId, FileID: req.Msg.FileId})
	return respondWith(result, err, FeaturedImageMessage)
}

func (h *ConnectHandler) DeletePageFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeletePageFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	runID, err := h.pages.DeletePageFeaturedImage(ctx, DeletePageFeaturedImageCommand{PageID: req.Msg.PageId})
	return respondWith(runID, err, FeaturedImageDeleteMessage)
}

func (h *ConnectHandler) CheckPageSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckPageSlugAvailableRequest]) (*connect.Response[managev1.CheckPageSlugAvailableResponse], error) {
	available, err := h.pages.CheckPageSlugAvailable(ctx, CheckPageSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeID: req.Msg.ExcludeId})
	return respond(&managev1.CheckPageSlugAvailableResponse{Available: available}, err)
}

func (h *ConnectHandler) ListPageVersions(ctx context.Context, req *connect.Request[managev1.ListPageVersionsRequest]) (*connect.Response[managev1.ListPageVersionsResponse], error) {
	pagination := req.Msg.GetPagination()
	list, err := h.pages.ListPageVersions(ctx, ListPageVersionsQuery{
		PageID: req.Msg.PageId, Page: Paging{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()},
	})
	return respondWith(list, err, VersionListMessage)
}

func (h *ConnectHandler) RestorePageVersion(ctx context.Context, req *connect.Request[managev1.RestorePageVersionRequest]) (*connect.Response[managev1.Page], error) {
	detail, err := h.pages.RestorePageVersion(ctx, RestorePageVersionCommand{PageID: req.Msg.PageId, VersionID: req.Msg.VersionId})
	return respondWith(detail, err, PageMessage)
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
	room, err := h.pages.LoadPageBlockDocument(ctx, LoadPageBlockDocumentQuery{
		PageID: req.Msg.PageId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	})
	return respondWith(room, err, roomDocumentMessage)
}

func (h *InternalConnectHandler) ApplyPageBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyPageBlockBatchRequest]) (*connect.Response[intrav1.ApplyPageBlockBatchResponse], error) {
	saved, err := h.pages.ApplyPageBlockBatch(ctx, ApplyPageBlockBatchCommand{
		PageID: req.Msg.GetPageId(), Locale: req.Msg.GetLocale(), Batch: req.Msg.GetBatch(),
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, AffectedLocaleValues: req.Msg.GetAffectedLocaleValues(),
	})
	return respondWith(saved, err, func(saved RoomSave) (*intrav1.ApplyPageBlockBatchResponse, error) {
		return &intrav1.ApplyPageBlockBatchResponse{
			DocumentRevision: saved.DocumentRevision, Changed: saved.Changed, SourceChanged: saved.SourceChanged,
			ChangedLocales: saved.ChangedLocales, Locale: saved.Locale, TargetRevision: saved.TargetRevision,
		}, nil
	})
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
	saved, err := h.pages.UpdatePageLocaleMetadata(ctx, command)
	return respondWith(saved, err, func(saved RoomSave) (*intrav1.UpdatePageLocaleMetadataResponse, error) {
		return &intrav1.UpdatePageLocaleMetadataResponse{
			DocumentRevision: saved.DocumentRevision, Changed: saved.Changed, SourceChanged: saved.SourceChanged,
			ChangedLocales: saved.ChangedLocales, Locale: saved.Locale, TargetRevision: saved.TargetRevision,
		}, nil
	})
}

func (h *InternalConnectHandler) UpdatePageDocumentMetadata(ctx context.Context, req *connect.Request[intrav1.UpdatePageDocumentMetadataRequest]) (*connect.Response[intrav1.UpdatePageDocumentMetadataResponse], error) {
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	saved, err := h.pages.UpdatePageDocumentMetadata(ctx, UpdatePageDocumentMetadataCommand{
		PageID: req.Msg.PageId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		DocumentLayout: documentLayoutInput(req.Msg.DocumentLayout), ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	return respondWith(saved, err, func(saved LayoutSave) (*intrav1.UpdatePageDocumentMetadataResponse, error) {
		return &intrav1.UpdatePageDocumentMetadataResponse{
			DocumentRevision: saved.DocumentRevision, Changed: saved.Changed,
			SourceChanged: saved.SourceChanged, Locale: saved.Locale,
		}, nil
	})
}

func (h *InternalConnectHandler) CreatePageVersionCheckpoint(ctx context.Context, req *connect.Request[intrav1.CreatePageVersionCheckpointRequest]) (*connect.Response[intrav1.CreatePageVersionCheckpointResponse], error) {
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	checkpoint, err := h.pages.CreatePageVersionCheckpoint(ctx, CreatePageVersionCheckpointCommand{
		PageID: req.Msg.PageId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	return respondWith(checkpoint, err, func(checkpoint Checkpoint) (*intrav1.CreatePageVersionCheckpointResponse, error) {
		return &intrav1.CreatePageVersionCheckpointResponse{
			Created: checkpoint.VersionID != nil, Revision: checkpoint.Revision, Locale: checkpoint.Locale,
			VersionId: checkpoint.VersionID,
		}, nil
	})
}

// AccessPolicyInput is the audience a request names; nil names none.
func AccessPolicyInput(message *commonv1.PageAccessPolicy) *pageaccess.Policy {
	if message == nil {
		return nil
	}
	policy := pageaccess.FromMessage(message)
	return &policy
}

// accessPolicyMessage decodes the stored audience of a Page.
func accessPolicyMessage(raw []byte) (*commonv1.PageAccessPolicy, error) {
	policy, err := pageaccess.Decode(raw)
	if err != nil {
		return nil, err
	}
	return pageaccess.Message(policy), nil
}

// PageMessage renders a Page as the manage API returns it.
func PageMessage(detail Detail) (*managev1.Page, error) {
	p := detail.Page
	accessPolicy, err := accessPolicyMessage(p.AccessPolicy)
	if err != nil {
		return nil, err
	}
	documentLayout, err := DocumentLayoutMessage(p.DocumentLayout)
	if err != nil {
		return nil, err
	}
	page := &managev1.Page{
		AccessPolicy:          accessPolicy,
		Id:                    p.ID,
		Title:                 detail.Title,
		SourceLocale:          p.SourceLocale,
		Status:                pageStatusToProto(p.Status),
		ShowTitle:             p.ShowTitle,
		DocumentLayout:        documentLayout,
		CreatedAt:             timestamppb.New(p.CreatedAt),
		UpdatedAt:             timestamppb.New(p.UpdatedAt),
		OgAsset:               mediaasset.ReferenceProto(detail.OgAsset),
		FeaturedImageDelivery: detail.FeaturedImage,
		Document:              detail.Document,
		Revision:              detail.Revision,
		BlockMedia:            detail.BlockMedia,
		Slug:                  p.Slug,
		Summary:               detail.Summary,
	}
	if p.PublishedAt != nil {
		page.PublishedAt = timestamppb.New(*p.PublishedAt)
	}
	return page, nil
}

// PageListMessage renders a page of Pages as the manage API lists them.
func PageListMessage(list PageList) (*managev1.ListPagesAdminResponse, error) {
	summaries := make([]*managev1.PageSummary, len(list.Pages))
	for i, view := range list.Pages {
		p := view.Page
		accessPolicy, err := accessPolicyMessage(p.AccessPolicy)
		if err != nil {
			return nil, err
		}
		summaries[i] = &managev1.PageSummary{
			AccessPolicy: accessPolicy,
			Id:           p.ID,
			Title:        view.Title,
			SourceLocale: p.SourceLocale,
			Status:       pageStatusToProto(p.Status),
			ShowTitle:    p.ShowTitle,
			CreatedAt:    timestamppb.New(p.CreatedAt),
			UpdatedAt:    timestamppb.New(p.UpdatedAt),
			Slug:         p.Slug,
		}
		if p.PublishedAt != nil {
			summaries[i].PublishedAt = timestamppb.New(*p.PublishedAt)
		}
	}
	return &managev1.ListPagesAdminResponse{Pages: summaries, Pagination: PaginationMessage(list.Pagination)}, nil
}

// PaginationMessage renders where a page sits among every match.
func PaginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

// UpdateResponseMessage renders the Page settings an update leaves.
func UpdateResponseMessage(updated UpdateResult) (*managev1.UpdatePageResponse, error) {
	page := updated.Page
	accessPolicy, err := accessPolicyMessage(page.AccessPolicy)
	if err != nil {
		return nil, err
	}
	return &managev1.UpdatePageResponse{
		Id:           page.ID,
		Changed:      updated.Changed,
		Slug:         page.Slug,
		ShowTitle:    page.ShowTitle,
		UpdatedAt:    timestamppb.New(page.UpdatedAt),
		AccessPolicy: accessPolicy,
	}, nil
}

// LifecycleMessage renders the Page a lifecycle transition leaves.
func LifecycleMessage(result LifecycleResult) (*managev1.PageLifecycleMutationResponse, error) {
	page := result.Page
	response := &managev1.PageLifecycleMutationResponse{
		Id:        page.ID,
		Changed:   result.Changed,
		Status:    pageStatusToProto(page.Status),
		UpdatedAt: timestamppb.New(page.UpdatedAt),
	}
	if page.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*page.PublishedAt)
	}
	return response, nil
}

// FeaturedImageMessage renders a set featured image.
func FeaturedImageMessage(result FeaturedImageResult) (*managev1.SetPageFeaturedImageResponse, error) {
	return &managev1.SetPageFeaturedImageResponse{ImageDelivery: result.Delivery, OgGenerationRunId: result.OgGenerationRunID}, nil
}

// FeaturedImageDeleteMessage renders a removed featured image.
func FeaturedImageDeleteMessage(runID *string) (*managev1.OgAssetDeleteResponse, error) {
	return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: runID}, nil
}

// VersionListMessage renders a page of Page version checkpoints.
func VersionListMessage(list VersionList) (*managev1.ListPageVersionsResponse, error) {
	versions := make([]*managev1.PageVersion, len(list.Versions))
	for i, view := range list.Versions {
		contributors := make([]*managev1.VersionContributor, 0, len(view.Contributors))
		for _, contributor := range view.Contributors {
			contributors = append(contributors, &managev1.VersionContributor{MemberId: contributor.MemberID, Nickname: contributor.Nickname})
		}
		versions[i] = &managev1.PageVersion{
			Id:            view.Version.ID,
			Version:       view.Version.Version,
			SourceLocale:  view.SourceLocale,
			Title:         view.Version.Title,
			CanonicalHash: view.CanonicalHash,
			CreatedAt:     timestamppb.New(view.Version.CreatedAt),
			Contributors:  contributors,
			Summary:       view.Version.Summary,
		}
	}
	return &managev1.ListPageVersionsResponse{Versions: versions, Pagination: PaginationMessage(list.Pagination)}, nil
}

func editorDocumentMessage(document EditorDocument) (*managev1.GetPageEditorDocumentResponse, error) {
	room := document.Room
	documentLayout, err := DocumentLayoutMessage(room.DocumentLayout)
	if err != nil {
		return nil, err
	}
	return &managev1.GetPageEditorDocumentResponse{
		Document: room.Document, DocumentRevision: room.DocumentRevision, Locale: room.Locale,
		LocaleExists: room.LocaleExists, TargetRevision: room.TargetRevision, SourceLocale: room.SourceMetadata.Locale,
		BlockMedia: room.BlockMedia, Editable: true, DocumentLayout: documentLayout,
		Title: document.Title, Summary: document.Summary,
	}, nil
}

func roomDocumentMessage(room RoomDocument) (*intrav1.LoadPageBlockDocumentResponse, error) {
	documentLayout, err := DocumentLayoutMessage(room.DocumentLayout)
	if err != nil {
		return nil, err
	}
	response := &intrav1.LoadPageBlockDocumentResponse{
		Document: room.Document, DocumentRevision: room.DocumentRevision,
		SourceMetadata: &intrav1.PageLocaleMetadata{Locale: room.SourceMetadata.Locale, Title: room.SourceMetadata.Title, Summary: room.SourceMetadata.Summary},
		Locale:         room.Locale, LocaleExists: room.LocaleExists,
		DocumentLayout:      documentLayout,
		BlockMedia:          room.BlockMedia,
		TargetRevision:      room.TargetRevision,
		PresentLocaleValues: room.PresentLocaleValues,
	}
	if metadata := room.LocaleMetadata; metadata != nil {
		response.LocaleMetadata = &intrav1.PageLocaleMetadata{Locale: metadata.Locale, Title: metadata.Title, Summary: metadata.Summary}
	}
	return response, nil
}

// pageStatusToProto maps a stored Page status to the API enum. A value the
// API does not know is UNSPECIFIED.
func pageStatusToProto(status Status) managev1.PageStatus {
	switch status {
	case StatusDraft:
		return managev1.PageStatus_PAGE_STATUS_DRAFT
	case StatusPublished:
		return managev1.PageStatus_PAGE_STATUS_PUBLISHED
	default:
		return managev1.PageStatus_PAGE_STATUS_UNSPECIFIED
	}
}

// pageStatusFromProto maps an API Page status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func pageStatusFromProto(status managev1.PageStatus) (Status, bool) {
	switch status {
	case managev1.PageStatus_PAGE_STATUS_DRAFT:
		return StatusDraft, true
	case managev1.PageStatus_PAGE_STATUS_PUBLISHED:
		return StatusPublished, true
	default:
		return "", false
	}
}

// DocumentLayoutMessage maps a stored layout without producing unspecified
// enum values. An invalid stored layout is an error so read paths can reject
// it without crashing the request (documentlayout.ToProto).
func DocumentLayoutMessage(l DocumentLayout) (*commonv1.DocumentLayout, error) {
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("invalid persisted document layout: %w", err)
	}
	result := &commonv1.DocumentLayout{HideTableOfContents: l.HideTableOfContents}
	switch l.ContentHeight {
	case ContentHeightContent:
		result.ContentHeight = commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT
	case ContentHeightViewport:
		result.ContentHeight = commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT
	}
	result.PageChrome = placementMessage(l.PageChrome)
	result.Footer = placementMessage(l.Footer)
	return result, nil
}

func placementMessage(placement RegionPlacement) commonv1.DocumentRegionPlacement {
	switch placement {
	case PlacementFlow:
		return commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW
	case PlacementPinned:
		return commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED
	default:
		return commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_UNSPECIFIED
	}
}

// documentLayoutInput is the layout a request names, nil when it names
// none; a value the domain does not know stays empty for
// RequireDocumentLayout to reject.
func documentLayoutInput(layout *commonv1.DocumentLayout) *DocumentLayout {
	if layout == nil {
		return nil
	}
	result := &DocumentLayout{HideTableOfContents: layout.HideTableOfContents}
	switch layout.ContentHeight {
	case commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_CONTENT:
		result.ContentHeight = ContentHeightContent
	case commonv1.DocumentContentHeight_DOCUMENT_CONTENT_HEIGHT_VIEWPORT:
		result.ContentHeight = ContentHeightViewport
	}
	result.PageChrome = placementInput(layout.PageChrome)
	result.Footer = placementInput(layout.Footer)
	return result
}

func placementInput(placement commonv1.DocumentRegionPlacement) RegionPlacement {
	switch placement {
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_FLOW:
		return PlacementFlow
	case commonv1.DocumentRegionPlacement_DOCUMENT_REGION_PLACEMENT_PINNED:
		return PlacementPinned
	default:
		return ""
	}
}

// DocumentLayoutFromMessage maps an API layout to its stored form, rejecting
// unspecified values (documentlayout.FromProto).
func DocumentLayoutFromMessage(layout *commonv1.DocumentLayout) (DocumentLayout, error) {
	return RequireDocumentLayout(documentLayoutInput(layout))
}
