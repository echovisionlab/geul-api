package emailauthoring

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/email"
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
	if err != nil {
		return nil, errs.Wrap(err)
	}
	templates := make([]*managev1.EmailTemplate, 0, len(page.Templates))
	for index := range page.Templates {
		templates = append(templates, emailTemplateMessage(page.Templates[index]))
	}
	return connect.NewResponse(&managev1.ListEmailTemplatesAdminResponse{Templates: templates, Pagination: page.Page.BuildResponse(page.Total)}), nil
}

func (h *TemplateConnectHandler) GetEmailTemplate(ctx context.Context, req *connect.Request[managev1.GetEmailTemplateRequest]) (*connect.Response[managev1.EmailTemplate], error) {
	template, err := h.authoring.GetEmailTemplate(ctx, GetEmailTemplateQuery{ID: req.Msg.Id})
	return respond(emailTemplateDocumentMessage(template), err)
}

func (h *TemplateConnectHandler) CreateEmailTemplate(ctx context.Context, req *connect.Request[managev1.CreateEmailTemplateRequest]) (*connect.Response[managev1.EmailTemplate], error) {
	template, err := h.authoring.CreateEmailTemplate(ctx, CreateEmailTemplateCommand{
		Key: req.Msg.Key, Name: req.Msg.Name, Subject: req.Msg.Subject,
		SourceLocale: req.Msg.SourceLocale, Description: req.Msg.Description,
	})
	return respond(emailTemplateDocumentMessage(template), err)
}

// emailTemplateMessage is the template with the verified metadata of a
// system template.
func emailTemplateMessage(template Template) *managev1.EmailTemplate {
	populateSystemEmailTemplateMetadata(&template)

	message := &managev1.EmailTemplate{
		Id:               template.ID,
		SourceLocale:     template.SourceLocale,
		Key:              template.Key,
		Name:             template.Name,
		Subject:          template.Subject,
		IsSystem:         template.IsSystem,
		IsActive:         template.IsActive,
		CreatedAt:        timestamppb.New(template.CreatedAt),
		DeliveryRunCount: template.DeliveryRunCount,
	}
	if template.Description != nil {
		message.Description = template.Description
	}
	if template.UpdatedAt != nil {
		message.UpdatedAt = timestamppb.New(*template.UpdatedAt)
	}
	if template.EventKey != nil {
		message.EventKey = template.EventKey
	}
	if template.LayoutID != nil {
		message.LayoutId = template.LayoutID
	}
	if template.Variables != nil {
		message.Variables = make([]*managev1.EmailTemplateVariable, len(template.Variables))
		for i, v := range template.Variables {
			message.Variables[i] = &managev1.EmailTemplateVariable{
				Name:        v.Name,
				Description: v.Description,
			}
			if v.DefaultValue != nil {
				message.Variables[i].DefaultValue = v.DefaultValue
			}
		}
	}
	return message
}

// emailTemplateDocumentMessage is the template with its source document.
func emailTemplateDocumentMessage(document TemplateDocument) *managev1.EmailTemplate {
	message := emailTemplateMessage(document.Template)
	message.Document = document.Projection.Document
	message.DocumentRevision = document.Projection.Revision
	message.SourceLocale = document.Projection.SourceLocale
	return message
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
	if err != nil {
		return nil, errs.Wrap(err)
	}
	messages := make([]*managev1.EmailEventMapping, 0, len(mappings))
	for _, mapping := range mappings {
		templateID, templateName := mapping.TemplateID, mapping.TemplateName
		messages = append(messages, &managev1.EmailEventMapping{
			Event: mapping.Event, TemplateId: &templateID, TemplateName: &templateName,
		})
	}
	return connect.NewResponse(&managev1.GetEventMappingsResponse{Mappings: messages}), nil
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
	layout, err := h.authoring.GetEmailLayout(ctx, GetEmailLayoutQuery{ID: req.Msg.Id})
	return respond(emailLayoutMessage(layout), err)
}

func emailLayoutMessage(layout Layout) *managev1.EmailLayout {
	return &managev1.EmailLayout{
		Id:               layout.ID,
		Name:             layout.Name,
		Key:              layout.Key,
		HtmlContent:      email.NormalizeTemplatePlaceholders(layout.HTMLContent),
		CreatedAt:        timestamppb.New(layout.CreatedAt),
		UpdatedAt:        timestamppb.New(layout.UpdatedAt),
		CampaignCount:    layout.CampaignCount,
		TemplateCount:    layout.TemplateCount,
		DeliveryRunCount: layout.DeliveryRunCount,
		SourceLocale:     layout.SourceLocale,
	}
}

func (h *LayoutConnectHandler) ListEmailLayoutsAdmin(ctx context.Context, req *connect.Request[managev1.ListEmailLayoutsAdminRequest]) (*connect.Response[managev1.ListEmailLayoutsAdminResponse], error) {
	page, err := h.authoring.ListEmailLayouts(ctx, ListEmailLayoutsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	layouts := make([]*managev1.EmailLayout, len(page.Layouts))
	for index := range page.Layouts {
		layouts[index] = emailLayoutMessage(page.Layouts[index])
	}
	return connect.NewResponse(&managev1.ListEmailLayoutsAdminResponse{Layouts: layouts, Pagination: page.Page.BuildResponse(page.Total)}), nil
}

func (h *LayoutConnectHandler) CreateEmailLayout(ctx context.Context, req *connect.Request[managev1.CreateEmailLayoutRequest]) (*connect.Response[managev1.EmailLayout], error) {
	layout, err := h.authoring.CreateEmailLayout(ctx, CreateEmailLayoutCommand{
		Name: req.Msg.Name, Key: req.Msg.Key, HTMLContent: req.Msg.HtmlContent, SourceLocale: req.Msg.SourceLocale,
	})
	return respond(emailLayoutMessage(layout), err)
}

func (h *LayoutConnectHandler) UpdateEmailLayout(ctx context.Context, req *connect.Request[managev1.UpdateEmailLayoutRequest]) (*connect.Response[managev1.EmailLayout], error) {
	layout, err := h.authoring.UpdateEmailLayout(ctx, UpdateEmailLayoutCommand{ID: req.Msg.Id, Name: req.Msg.Name, Key: req.Msg.Key})
	return respond(emailLayoutMessage(layout), err)
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
	var issues []*managev1.EmailLayoutValidationError
	for _, issue := range preview.Errors {
		issues = append(issues, &managev1.EmailLayoutValidationError{Code: issue.Code, Message: issue.Message})
	}
	return respond(&managev1.PreviewEmailLayoutContentResponse{Valid: preview.Valid, Html: preview.HTML, Errors: issues}, err)
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
		TemplateID: req.Msg.GetEmailTemplateId(), Locale: req.Msg.GetLocale(), SessionID: req.Msg.GetPrincipal().GetSessionId(),
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
	subject := ptrStringValue(state.SourceMetadata.Subject)
	return &intrav1.EmailTemplateLocaleMetadata{Locale: state.SourceLocale, Subject: &subject}
}

func emailTemplateLocaleMetadataProjection(state emailcontent.ExactLocaleState, locale string) *intrav1.EmailTemplateLocaleMetadata {
	if state.TargetMetadata == nil {
		return nil
	}
	subject := ptrStringValue(state.SourceMetadata.Subject)
	if state.TargetMetadata.Subject != nil {
		subject = *state.TargetMetadata.Subject
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
		LocaleValues: layoutUnitValues(req.Msg.LocaleValues), Contributors: req.Msg.ContributorMemberIds,
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
		Units: layoutContentUnitMessages(document.Units), LocaleValues: layoutLocaleValueMessages(document.LocaleValues),
	}, err)
}

// layoutUnitValues keeps a nil request entry nil, so the save rejects it.
func layoutUnitValues(values []*intrav1.EmailLayoutLocaleValue) []*LayoutUnitValue {
	if values == nil {
		return nil
	}
	converted := make([]*LayoutUnitValue, len(values))
	for index, value := range values {
		if value != nil {
			converted[index] = &LayoutUnitValue{Handle: value.Handle, Value: value.Value}
		}
	}
	return converted
}

func layoutContentUnitMessages(units []LayoutContentUnit) []*intrav1.EmailLayoutContentUnit {
	if units == nil {
		return nil
	}
	messages := make([]*intrav1.EmailLayoutContentUnit, 0, len(units))
	for _, unit := range units {
		messages = append(messages, &intrav1.EmailLayoutContentUnit{
			Handle: unit.Handle, Kind: unit.Kind, Element: unit.Element,
			Attribute: unit.Attribute, Order: unit.Order, SourceValue: unit.SourceValue,
		})
	}
	return messages
}

func layoutLocaleValueMessages(values []LayoutUnitValue) []*intrav1.EmailLayoutLocaleValue {
	if values == nil {
		return nil
	}
	messages := make([]*intrav1.EmailLayoutLocaleValue, 0, len(values))
	for _, value := range values {
		messages = append(messages, &intrav1.EmailLayoutLocaleValue{Handle: value.Handle, Value: value.Value})
	}
	return messages
}
