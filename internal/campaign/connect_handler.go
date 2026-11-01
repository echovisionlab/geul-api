package campaign

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

// ConnectHandler serves the Campaign Connect RPCs. Each method translates
// the request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedCampaignServiceHandler
	campaigns *Service
}

// NewConnectHandler returns the Connect adapter of campaigns.
func NewConnectHandler(campaigns *Service) *ConnectHandler {
	return &ConnectHandler{campaigns: campaigns}
}

func (h *ConnectHandler) GetCampaign(ctx context.Context, req *connect.Request[managev1.GetCampaignRequest]) (*connect.Response[managev1.GetCampaignResponse], error) {
	campaign, err := h.campaigns.GetCampaign(ctx, GetCampaignQuery{ID: req.Msg.Id})
	return respond(&managev1.GetCampaignResponse{Campaign: campaign}, err)
}

func (h *ConnectHandler) ListCampaignsAdmin(ctx context.Context, req *connect.Request[managev1.ListCampaignsAdminRequest]) (*connect.Response[managev1.ListCampaignsAdminResponse], error) {
	page, err := h.campaigns.ListCampaigns(ctx, ListCampaignsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListCampaignsAdminResponse{Campaigns: page.Campaigns, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) CreateCampaign(ctx context.Context, req *connect.Request[managev1.CreateCampaignRequest]) (*connect.Response[managev1.CreateCampaignResponse], error) {
	command := CreateCampaignCommand{Name: req.Msg.Name, Subject: req.Msg.Subject, SourceLocale: req.Msg.SourceLocale}
	switch target := req.Msg.Target.(type) {
	case *managev1.CreateCampaignRequest_All:
		command.Target = CreateCampaignTarget{All: target.All != nil, allMissing: target.All == nil}
	case *managev1.CreateCampaignRequest_SegmentId:
		command.Target = CreateCampaignTarget{SegmentID: &target.SegmentId}
	}
	campaign, err := h.campaigns.CreateCampaign(ctx, command)
	return respond(&managev1.CreateCampaignResponse{Campaign: campaign}, err)
}

func (h *ConnectHandler) UpdateCampaignName(ctx context.Context, req *connect.Request[managev1.UpdateCampaignNameRequest]) (*connect.Response[managev1.UpdateCampaignNameResponse], error) {
	update, err := h.campaigns.UpdateCampaignName(ctx, UpdateCampaignNameCommand{ID: req.Msg.Id, Name: req.Msg.Name})
	return respond(&managev1.UpdateCampaignNameResponse{
		Id: update.ID, Changed: update.Changed, Name: update.Name, UpdatedAt: timestamppb.New(update.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) UpdateCampaignConfiguration(ctx context.Context, req *connect.Request[managev1.UpdateCampaignConfigurationRequest]) (*connect.Response[managev1.UpdateCampaignConfigurationResponse], error) {
	configuration, err := h.campaigns.UpdateCampaignConfiguration(ctx, UpdateCampaignConfigurationCommand{
		ID: req.Msg.Id, TargetMode: req.Msg.TargetMode, SegmentID: req.Msg.SegmentId,
		RecipientScope: req.Msg.RecipientScope, LayoutID: req.Msg.LayoutId,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	recipientScope, err := campaignRecipientScopeToProto(configuration.RecipientScope)
	return respond(&managev1.UpdateCampaignConfigurationResponse{
		Id: configuration.ID, Changed: configuration.Changed,
		TargetMode: campaignTargetModeToProto(configuration.TargetMode), SegmentId: configuration.SegmentID,
		LayoutId: configuration.LayoutID, RecipientScope: recipientScope, UpdatedAt: timestamppb.New(configuration.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) DeleteCampaign(ctx context.Context, req *connect.Request[managev1.DeleteCampaignRequest]) (*connect.Response[managev1.DeleteCampaignResponse], error) {
	err := h.campaigns.DeleteCampaign(ctx, DeleteCampaignCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteCampaignResponse{Success: true}, err)
}

func (h *ConnectHandler) ScheduleCampaign(ctx context.Context, req *connect.Request[managev1.ScheduleCampaignRequest]) (*connect.Response[managev1.ScheduleCampaignResponse], error) {
	command := ScheduleCampaignCommand{ID: req.Msg.Id, RecipientScope: req.Msg.GetRecipientScope()}
	if req.Msg.ScheduledAt != nil {
		scheduledAt := req.Msg.ScheduledAt.AsTime()
		command.ScheduledAt = &scheduledAt
	}
	lifecycle, err := h.campaigns.ScheduleCampaign(ctx, command)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.ScheduleCampaignResponse{
		Id: lifecycle.ID, Changed: true, Status: campaignStatusToProto(lifecycle.Status),
		ScheduledAt: timestamppb.New(*lifecycle.ScheduledAt), RecipientScope: req.Msg.GetRecipientScope(),
		UpdatedAt: timestamppb.New(lifecycle.UpdatedAt),
	}), nil
}

func (h *ConnectHandler) CancelCampaign(ctx context.Context, req *connect.Request[managev1.CancelCampaignRequest]) (*connect.Response[managev1.CancelCampaignResponse], error) {
	lifecycle, err := h.campaigns.CancelCampaign(ctx, CancelCampaignCommand{ID: req.Msg.Id})
	return respond(&managev1.CancelCampaignResponse{
		Id: lifecycle.ID, Changed: true, Status: campaignStatusToProto(lifecycle.Status),
		UpdatedAt: timestamppb.New(lifecycle.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) SendCampaignNow(ctx context.Context, req *connect.Request[managev1.SendCampaignNowRequest]) (*connect.Response[managev1.SendCampaignNowResponse], error) {
	lifecycle, err := h.campaigns.SendCampaignNow(ctx, SendCampaignNowCommand{ID: req.Msg.Id, RecipientScope: req.Msg.GetRecipientScope()})
	return respond(&managev1.SendCampaignNowResponse{
		Id: lifecycle.ID, Status: campaignStatusToProto(lifecycle.Status),
		RecipientCount: int32(lifecycle.RecipientCount), UpdatedAt: timestamppb.New(lifecycle.UpdatedAt),
	}, err)
}

func (h *ConnectHandler) PreviewCampaign(ctx context.Context, req *connect.Request[managev1.PreviewCampaignRequest]) (*connect.Response[managev1.PreviewCampaignResponse], error) {
	preview, err := h.campaigns.PreviewCampaign(ctx, PreviewCampaignQuery{
		ID: req.Msg.Id, Locale: req.Msg.Locale, Subject: req.Msg.Subject,
		Document: req.Msg.GetDocument(), LayoutID: req.Msg.LayoutId,
	})
	return respond(&managev1.PreviewCampaignResponse{Preview: &managev1.CampaignPreview{
		Subject: preview.Subject, HtmlContent: preview.HTMLContent, TextContent: preview.TextContent,
	}}, err)
}

func (h *ConnectHandler) SendTestCampaign(ctx context.Context, req *connect.Request[managev1.SendTestCampaignRequest]) (*connect.Response[managev1.SendTestCampaignResponse], error) {
	err := h.campaigns.SendTestCampaign(ctx, SendTestCampaignCommand{ID: req.Msg.Id, Email: req.Msg.Email, Locale: req.Msg.Locale})
	return respond(&managev1.SendTestCampaignResponse{Success: true}, err)
}

func (h *ConnectHandler) GetCampaignStats(ctx context.Context, req *connect.Request[managev1.GetCampaignStatsRequest]) (*connect.Response[managev1.GetCampaignStatsResponse], error) {
	stats, err := h.campaigns.GetCampaignStats(ctx, GetCampaignStatsQuery{ID: req.Msg.Id})
	return respond(&managev1.GetCampaignStatsResponse{Stats: stats}, err)
}

func (h *ConnectHandler) GetCampaignRecipients(ctx context.Context, req *connect.Request[managev1.GetCampaignRecipientsRequest]) (*connect.Response[managev1.GetCampaignRecipientsResponse], error) {
	page, err := h.campaigns.GetCampaignRecipients(ctx, GetCampaignRecipientsQuery{ID: req.Msg.Id, Limit: req.Msg.Limit, Offset: req.Msg.Offset})
	return respond(&managev1.GetCampaignRecipientsResponse{Recipients: page.Recipients, Total: page.Total}, err)
}

// InternalConnectHandler serves the Campaign collaboration RPCs.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalCampaignServiceHandler
	documents *InternalService
}

// NewInternalConnectHandler returns the Campaign collaboration adapter of
// documents.
func NewInternalConnectHandler(documents *InternalService) *InternalConnectHandler {
	return &InternalConnectHandler{documents: documents}
}

func (h *InternalConnectHandler) ApplyBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyCampaignBlockBatchRequest]) (*connect.Response[intrav1.ApplyCampaignBlockBatchResponse], error) {
	change, err := h.documents.ApplyCampaignBlockBatch(ctx, ApplyCampaignBlockBatchCommand{
		CampaignID: req.Msg.GetCampaignId(), Locale: req.Msg.GetLocale(),
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, Batch: req.Msg.GetBatch(),
		AffectedLocaleValues: req.Msg.GetAffectedLocaleValues(),
	})
	return respond(&intrav1.ApplyCampaignBlockBatchResponse{
		DocumentRevision: change.DocumentRevision, Changed: change.Changed, SourceChanged: change.SourceChanged,
		ChangedLocales: change.ChangedLocales, Locale: change.Locale, TargetRevision: change.TargetRevision,
	}, err)
}

func (h *InternalConnectHandler) UpdateCampaignLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateCampaignLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateCampaignLocaleMetadataResponse], error) {
	change, err := h.documents.UpdateCampaignLocaleMetadata(ctx, UpdateCampaignLocaleMetadataCommand{
		CampaignID: req.Msg.GetCampaignId(), Locale: req.Msg.GetLocale(),
		ExpectedRevision: req.Msg.GetExpectedRevision(), ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Subject: req.Msg.Subject, Contributors: req.Msg.GetContributorMemberIds(),
	})
	return respond(&intrav1.UpdateCampaignLocaleMetadataResponse{
		DocumentRevision: change.DocumentRevision, Changed: change.Changed, SourceChanged: change.SourceChanged,
		ChangedLocales: change.ChangedLocales, Locale: change.Locale, TargetRevision: change.TargetRevision,
	}, err)
}

func (h *InternalConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadCampaignDocumentRequest]) (*connect.Response[intrav1.LoadCampaignDocumentResponse], error) {
	document, err := h.documents.LoadCampaignDocument(ctx, LoadCampaignDocumentQuery{
		CampaignID: req.Msg.GetCampaignId(), Locale: req.Msg.GetLocale(), Principal: req.Msg.GetPrincipal(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	state := document.State
	return connect.NewResponse(&intrav1.LoadCampaignDocumentResponse{
		Document: document.Document, DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: campaignSourceMetadataProjection(state), Locale: document.Locale,
		LocaleExists:   state.TargetMetadata != nil,
		LocaleMetadata: campaignLocaleMetadataProjection(state, document.Locale), TargetRevision: document.TargetRevision,
		PresentLocaleValues: document.PresentLocaleValues,
	}), nil
}

func campaignLocaleMetadataProjection(state emailcontent.ExactLocaleState, locale string) *intrav1.CampaignLocaleMetadata {
	if state.TargetMetadata == nil {
		return nil
	}
	subject := state.SourceMetadata.Subject.String
	if state.TargetMetadata.Subject.Valid {
		subject = state.TargetMetadata.Subject.String
	}
	return &intrav1.CampaignLocaleMetadata{Locale: locale, Subject: &subject}
}

func campaignSourceMetadataProjection(state emailcontent.ExactLocaleState) *intrav1.CampaignLocaleMetadata {
	subject := state.SourceMetadata.Subject.String
	return &intrav1.CampaignLocaleMetadata{Locale: state.SourceLocale, Subject: &subject}
}
