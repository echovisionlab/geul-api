package emailauthoring

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// TemplateConnectHandler serves the Email Template Connect RPCs. Each method
// translates the request into a use-case command or query, and the result
// into the response; errs.Wrap maps use-case errors to Connect codes.
type TemplateConnectHandler struct {
	managev1connect.UnimplementedEmailTemplateServiceHandler
	authoring *Service
}

// NewTemplateConnectHandler returns the Email Template Connect adapter of
// authoring.
func NewTemplateConnectHandler(authoring *Service) *TemplateConnectHandler {
	return &TemplateConnectHandler{authoring: authoring}
}

func (h *TemplateConnectHandler) ListEmailTemplatesAdmin(ctx context.Context, req *connect.Request[managev1.ListEmailTemplatesAdminRequest]) (*connect.Response[managev1.ListEmailTemplatesAdminResponse], error) {
	page, err := h.authoring.ListEmailTemplates(ctx, ListEmailTemplatesQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListEmailTemplatesAdminResponse{Templates: page.Templates, Pagination: page.Pagination}, err)
}

func (h *TemplateConnectHandler) GetEmailTemplate(ctx context.Context, req *connect.Request[managev1.GetEmailTemplateRequest]) (*connect.Response[managev1.EmailTemplate], error) {
	return respond(h.authoring.GetEmailTemplate(ctx, GetEmailTemplateQuery{ID: req.Msg.Id}))
}

func (h *TemplateConnectHandler) CreateEmailTemplate(ctx context.Context, req *connect.Request[managev1.CreateEmailTemplateRequest]) (*connect.Response[managev1.EmailTemplate], error) {
	return respond(h.authoring.CreateEmailTemplate(ctx, CreateEmailTemplateCommand{
		Key: req.Msg.Key, Name: req.Msg.Name, Subject: req.Msg.Subject,
		SourceLocale: req.Msg.SourceLocale, Description: req.Msg.Description,
	}))
}

func (h *TemplateConnectHandler) UpdateEmailTemplate(ctx context.Context, req *connect.Request[managev1.UpdateEmailTemplateRequest]) (*connect.Response[managev1.UpdateEmailTemplateResponse], error) {
	update, err := h.authoring.UpdateEmailTemplate(ctx, UpdateEmailTemplateCommand{
		ID: req.Msg.Id, Name: req.Msg.Name, Description: req.Msg.Description,
		IsActive: req.Msg.IsActive, LayoutID: req.Msg.LayoutId,
	})
	return respond(&managev1.UpdateEmailTemplateResponse{
		Id: update.ID, Changed: update.Changed, Name: update.Name, Description: update.Description,
		IsActive: update.IsActive, LayoutId: update.LayoutID, UpdatedAt: timestamppb.New(update.UpdatedAt),
	}, err)
}

func (h *TemplateConnectHandler) DeleteEmailTemplate(ctx context.Context, req *connect.Request[managev1.DeleteEmailTemplateRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.authoring.DeleteEmailTemplate(ctx, DeleteEmailTemplateCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *TemplateConnectHandler) GetEventMappings(ctx context.Context, _ *connect.Request[managev1.GetEventMappingsRequest]) (*connect.Response[managev1.GetEventMappingsResponse], error) {
	mappings, err := h.authoring.GetEventMappings(ctx)
	return respond(&managev1.GetEventMappingsResponse{Mappings: mappings}, err)
}

func (h *TemplateConnectHandler) UpdateEventMapping(ctx context.Context, req *connect.Request[managev1.UpdateEventMappingRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	err := h.authoring.UpdateEventMapping(ctx, UpdateEventMappingCommand{Event: req.Msg.Event, TemplateID: req.Msg.TemplateId})
	return respond(&managev1.SuccessResponse{Success: true}, err)
}

func (h *TemplateConnectHandler) SendTestEmail(ctx context.Context, req *connect.Request[managev1.SendTestEmailRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	err := h.authoring.SendTestEmail(ctx, SendTestEmailCommand{ID: req.Msg.Id, Email: req.Msg.Email, Locale: req.Msg.Locale})
	return respond(&managev1.SuccessResponse{Success: true}, err)
}

func (h *TemplateConnectHandler) PreviewEmailTemplate(ctx context.Context, req *connect.Request[managev1.PreviewEmailTemplateRequest]) (*connect.Response[managev1.PreviewEmailTemplateResponse], error) {
	preview, err := h.authoring.PreviewEmailTemplate(ctx, PreviewEmailTemplateQuery{
		ID: req.Msg.Id, Locale: req.Msg.Locale, Subject: req.Msg.Subject,
		Document: req.Msg.GetDocument(), LayoutID: req.Msg.LayoutId,
	})
	return respond(&managev1.PreviewEmailTemplateResponse{Subject: preview.Subject, Html: preview.HTML}, err)
}

// LayoutConnectHandler serves the Email Layout Connect RPCs.
type LayoutConnectHandler struct {
	managev1connect.UnimplementedEmailLayoutServiceHandler
	authoring *Service
}

// NewLayoutConnectHandler returns the Email Layout Connect adapter of
// authoring.
func NewLayoutConnectHandler(authoring *Service) *LayoutConnectHandler {
	return &LayoutConnectHandler{authoring: authoring}
}

func (h *LayoutConnectHandler) GetEmailLayout(ctx context.Context, req *connect.Request[managev1.GetEmailLayoutRequest]) (*connect.Response[managev1.EmailLayout], error) {
	return respond(h.authoring.GetEmailLayout(ctx, GetEmailLayoutQuery{ID: req.Msg.Id}))
}

func (h *LayoutConnectHandler) ListEmailLayoutsAdmin(ctx context.Context, req *connect.Request[managev1.ListEmailLayoutsAdminRequest]) (*connect.Response[managev1.ListEmailLayoutsAdminResponse], error) {
	page, err := h.authoring.ListEmailLayouts(ctx, ListEmailLayoutsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListEmailLayoutsAdminResponse{Layouts: page.Layouts, Pagination: page.Pagination}, err)
}

func (h *LayoutConnectHandler) CreateEmailLayout(ctx context.Context, req *connect.Request[managev1.CreateEmailLayoutRequest]) (*connect.Response[managev1.EmailLayout], error) {
	return respond(h.authoring.CreateEmailLayout(ctx, CreateEmailLayoutCommand{
		Name: req.Msg.Name, Key: req.Msg.Key, HTMLContent: req.Msg.HtmlContent, SourceLocale: req.Msg.SourceLocale,
	}))
}

func (h *LayoutConnectHandler) UpdateEmailLayout(ctx context.Context, req *connect.Request[managev1.UpdateEmailLayoutRequest]) (*connect.Response[managev1.EmailLayout], error) {
	return respond(h.authoring.UpdateEmailLayout(ctx, UpdateEmailLayoutCommand{ID: req.Msg.Id, Name: req.Msg.Name, Key: req.Msg.Key}))
}

func (h *LayoutConnectHandler) DeleteEmailLayout(ctx context.Context, req *connect.Request[managev1.DeleteEmailLayoutRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.authoring.DeleteEmailLayout(ctx, DeleteEmailLayoutCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *LayoutConnectHandler) PreviewEmailLayout(ctx context.Context, req *connect.Request[managev1.PreviewEmailLayoutRequest]) (*connect.Response[managev1.PreviewEmailLayoutResponse], error) {
	html, err := h.authoring.PreviewEmailLayout(ctx, PreviewEmailLayoutQuery{
		ID: req.Msg.Id, SampleContent: req.Msg.SampleContent, Locale: req.Msg.Locale,
	})
	return respond(&managev1.PreviewEmailLayoutResponse{Html: html}, err)
}

func (h *LayoutConnectHandler) PreviewEmailLayoutContent(ctx context.Context, req *connect.Request[managev1.PreviewEmailLayoutContentRequest]) (*connect.Response[managev1.PreviewEmailLayoutContentResponse], error) {
	preview, err := h.authoring.PreviewEmailLayoutContent(ctx, PreviewEmailLayoutContentQuery{
		HTMLContent: req.Msg.HtmlContent, SampleContent: req.Msg.SampleContent, Locale: req.Msg.Locale,
	})
	return respond(&managev1.PreviewEmailLayoutContentResponse{Valid: preview.Valid, Html: preview.HTML, Errors: preview.Errors}, err)
}

// InternalTemplateConnectHandler serves the Email Template collaboration
// RPCs.
type InternalTemplateConnectHandler struct {
	intrav1connect.UnimplementedInternalEmailTemplateServiceHandler
	documents *InternalService
}

// NewInternalTemplateConnectHandler returns the Email Template collaboration
// adapter of documents.
func NewInternalTemplateConnectHandler(documents *InternalService) *InternalTemplateConnectHandler {
	return &InternalTemplateConnectHandler{documents: documents}
}

func (h *InternalTemplateConnectHandler) ApplyBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyEmailTemplateBlockBatchRequest]) (*connect.Response[intrav1.ApplyEmailTemplateBlockBatchResponse], error) {
	change, err := h.documents.ApplyTemplateBlockBatch(ctx, ApplyTemplateBlockBatchCommand{
		TemplateID: req.Msg.GetEmailTemplateId(), Locale: req.Msg.GetLocale(),
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, Batch: req.Msg.GetBatch(),
		AffectedLocaleValues: req.Msg.GetAffectedLocaleValues(),
	})
	return respond(&intrav1.ApplyEmailTemplateBlockBatchResponse{
		DocumentRevision: change.DocumentRevision, Changed: change.Changed, SourceChanged: change.SourceChanged,
		ChangedLocales: change.ChangedLocales, Locale: change.Locale, TargetRevision: change.TargetRevision,
	}, err)
}

func (h *InternalTemplateConnectHandler) UpdateEmailTemplateLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateEmailTemplateLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateEmailTemplateLocaleMetadataResponse], error) {
	change, err := h.documents.UpdateTemplateLocaleMetadata(ctx, UpdateTemplateLocaleMetadataCommand{
		TemplateID: req.Msg.GetEmailTemplateId(), Locale: req.Msg.GetLocale(),
		ExpectedRevision: req.Msg.GetExpectedRevision(), ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Subject: req.Msg.Subject, Contributors: req.Msg.GetContributorMemberIds(),
	})
	return respond(&intrav1.UpdateEmailTemplateLocaleMetadataResponse{
		DocumentRevision: change.DocumentRevision, Changed: change.Changed, SourceChanged: change.SourceChanged,
		ChangedLocales: change.ChangedLocales, Locale: change.Locale, TargetRevision: change.TargetRevision,
	}, err)
}

func (h *InternalTemplateConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadEmailTemplateDocumentRequest]) (*connect.Response[intrav1.LoadEmailTemplateDocumentResponse], error) {
	document, err := h.documents.LoadTemplateDocument(ctx, LoadTemplateDocumentQuery{
		TemplateID: req.Msg.GetEmailTemplateId(), Locale: req.Msg.GetLocale(), Principal: req.Msg.GetPrincipal(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	state := document.State
	return connect.NewResponse(&intrav1.LoadEmailTemplateDocumentResponse{
		Document: document.Document, DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: emailTemplateSourceMetadataProjection(state), Locale: document.Locale,
		LocaleExists:   state.TargetMetadata != nil,
		LocaleMetadata: emailTemplateLocaleMetadataProjection(state, document.Locale), TargetRevision: document.TargetRevision,
		PresentLocaleValues: document.PresentLocaleValues,
	}), nil
}

func emailTemplateSourceMetadataProjection(state emailcontent.ExactLocaleState) *intrav1.EmailTemplateLocaleMetadata {
	subject := state.SourceMetadata.Subject.String
	return &intrav1.EmailTemplateLocaleMetadata{Locale: state.SourceLocale, Subject: &subject}
}

func emailTemplateLocaleMetadataProjection(state emailcontent.ExactLocaleState, locale string) *intrav1.EmailTemplateLocaleMetadata {
	if state.TargetMetadata == nil {
		return nil
	}
	subject := state.SourceMetadata.Subject.String
	if state.TargetMetadata.Subject.Valid {
		subject = state.TargetMetadata.Subject.String
	}
	return &intrav1.EmailTemplateLocaleMetadata{Locale: locale, Subject: &subject}
}

// InternalLayoutConnectHandler serves the Email Layout collaboration RPCs.
type InternalLayoutConnectHandler struct {
	intrav1connect.UnimplementedInternalEmailLayoutServiceHandler
	documents *InternalService
}

// NewInternalLayoutConnectHandler returns the Email Layout collaboration
// adapter of documents.
func NewInternalLayoutConnectHandler(documents *InternalService) *InternalLayoutConnectHandler {
	return &InternalLayoutConnectHandler{documents: documents}
}

func (h *InternalLayoutConnectHandler) SaveDocument(ctx context.Context, req *connect.Request[intrav1.SaveEmailLayoutDocumentRequest]) (*connect.Response[intrav1.SaveEmailLayoutDocumentResponse], error) {
	saved, err := h.documents.SaveLayoutDocument(ctx, SaveLayoutDocumentCommand{
		LayoutID: req.Msg.EmailLayoutId, Locale: req.Msg.Locale,
		ExpectedDocumentRevision: req.Msg.ExpectedDocumentRevision, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		ContentHTML: req.Msg.ContentHtml, ContentText: req.Msg.ContentText,
		LocaleValues: req.Msg.LocaleValues, Contributors: req.Msg.ContributorMemberIds,
	})
	return respond(&intrav1.SaveEmailLayoutDocumentResponse{
		Success: true, Locale: saved.Locale, DocumentRevision: saved.DocumentRevision, TargetRevision: saved.TargetRevision,
	}, err)
}

func (h *InternalLayoutConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadEmailLayoutDocumentRequest]) (*connect.Response[intrav1.LoadEmailLayoutDocumentResponse], error) {
	document, err := h.documents.LoadLayoutDocument(ctx, LoadLayoutDocumentQuery{LayoutID: req.Msg.EmailLayoutId, Locale: req.Msg.Locale})
	return respond(&intrav1.LoadEmailLayoutDocumentResponse{
		ContentHtml: document.ContentHTML, ContentText: document.ContentText,
		SourceLocale: document.SourceLocale, Locale: document.Locale,
		DocumentRevision: document.DocumentRevision, TargetRevision: document.TargetRevision,
		Units: document.Units, LocaleValues: document.LocaleValues,
	}, err)
}
