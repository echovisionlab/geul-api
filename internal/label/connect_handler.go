package label

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the manage Label RPCs. Each method translates the
// request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedLabelServiceHandler
	labels *Service
}

// NewConnectHandler returns the manage Connect adapter of labels.
func NewConnectHandler(labels *Service) *ConnectHandler {
	return &ConnectHandler{labels: labels}
}

// InternalConnectHandler serves the collaboration service's Label document
// RPCs.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalLabelServiceHandler
	labels *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of labels.
func NewInternalConnectHandler(labels *Service) *InternalConnectHandler {
	return &InternalConnectHandler{labels: labels}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) GetLabelEditorData(ctx context.Context, req *connect.Request[managev1.GetLabelEditorDataRequest]) (*connect.Response[managev1.GetLabelEditorDataResponse], error) {
	data, err := h.labels.GetLabelEditorData(ctx, GetLabelEditorDataQuery{ID: req.Msg.Id})
	return respond(&managev1.GetLabelEditorDataResponse{Label: data.Label, AllowedActions: data.AllowedActions}, err)
}

func (h *ConnectHandler) ListLabels(ctx context.Context, req *connect.Request[managev1.ListLabelsRequest]) (*connect.Response[managev1.ListLabelsResponse], error) {
	page, err := h.labels.ListLabels(ctx, ListLabelsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListLabelsResponse{Labels: page.Labels, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) ListLabelsAdmin(ctx context.Context, req *connect.Request[managev1.ListLabelsAdminRequest]) (*connect.Response[managev1.ListLabelsAdminResponse], error) {
	page, err := h.labels.ListLabelsAdmin(ctx, ListLabelsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListLabelsAdminResponse{Labels: page.Labels, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) CreateLabel(ctx context.Context, req *connect.Request[managev1.CreateLabelRequest]) (*connect.Response[managev1.Label], error) {
	return respond(h.labels.CreateLabel(ctx, CreateLabelCommand{
		Name: req.Msg.Name, Slug: req.Msg.Slug, CountryCode: req.Msg.CountryCode, Website: req.Msg.Website,
		ParentLabelID: req.Msg.ParentLabelId, SocialLinks: req.Msg.SocialLinks, Document: req.Msg.Document,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) DeleteLabel(ctx context.Context, req *connect.Request[managev1.DeleteLabelRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.labels.DeleteLabel(ctx, DeleteLabelCommand{ID: req.Msg.Id, PreviewRevision: req.Msg.PreviewRevision})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) PreviewDeleteLabel(ctx context.Context, req *connect.Request[managev1.PreviewDeleteLabelRequest]) (*connect.Response[managev1.PreviewDeleteLabelResponse], error) {
	return respond(h.labels.PreviewDeleteLabel(ctx, PreviewDeleteLabelQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishLabel(ctx context.Context, req *connect.Request[managev1.PublishLabelRequest]) (*connect.Response[managev1.LabelLifecycleMutationResponse], error) {
	return respond(h.labels.PublishLabel(ctx, PublishLabelCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) UnpublishLabel(ctx context.Context, req *connect.Request[managev1.UnpublishLabelRequest]) (*connect.Response[managev1.LabelLifecycleMutationResponse], error) {
	return respond(h.labels.UnpublishLabel(ctx, UnpublishLabelCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) SetLabelImage(ctx context.Context, req *connect.Request[managev1.SetLabelImageRequest]) (*connect.Response[managev1.SetLabelImageResponse], error) {
	update, err := h.labels.SetLabelImage(ctx, SetLabelImageCommand{LabelID: req.Msg.LabelId, FileID: req.Msg.FileId, Variant: logoVariant(req.Msg.Variant)})
	return respond(&managev1.SetLabelImageResponse{
		ImageLightAsset: update.Light, ImageDarkAsset: update.Dark, OgGenerationRunId: update.OgGenerationRunID,
	}, err)
}

func (h *ConnectHandler) DeleteLabelImage(ctx context.Context, req *connect.Request[managev1.DeleteLabelImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	runID, err := h.labels.DeleteLabelImage(ctx, DeleteLabelImageCommand{LabelID: req.Msg.LabelId, Variant: logoVariant(req.Msg.Variant)})
	return respond(&managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: runID}, err)
}

func (h *ConnectHandler) ListLabelParticipants(ctx context.Context, req *connect.Request[managev1.ListLabelParticipantsRequest]) (*connect.Response[managev1.ListLabelParticipantsResponse], error) {
	participants, err := h.labels.ListLabelParticipants(ctx, ListLabelParticipantsQuery{LabelID: req.Msg.LabelId})
	return respond(&managev1.ListLabelParticipantsResponse{Participants: participants}, err)
}

func (h *ConnectHandler) SetLabelParticipant(ctx context.Context, req *connect.Request[managev1.SetLabelParticipantRequest]) (*connect.Response[managev1.LabelParticipant], error) {
	return respond(h.labels.SetLabelParticipant(ctx, SetLabelParticipantCommand{
		LabelID: req.Msg.LabelId, MemberID: req.Msg.MemberId, Role: participantRole(req.Msg.Role),
	}))
}

func (h *ConnectHandler) RemoveLabelParticipant(ctx context.Context, req *connect.Request[managev1.RemoveLabelParticipantRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.labels.RemoveLabelParticipant(ctx, RemoveLabelParticipantCommand{LabelID: req.Msg.LabelId, MemberID: req.Msg.MemberId})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) CheckLabelSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckLabelSlugAvailableRequest]) (*connect.Response[managev1.CheckLabelSlugAvailableResponse], error) {
	available, err := h.labels.CheckLabelSlugAvailable(ctx, CheckLabelSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeLabelID: req.Msg.ExcludeLabelId})
	return respond(&managev1.CheckLabelSlugAvailableResponse{Available: available}, err)
}

func (h *InternalConnectHandler) LoadLabelBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadLabelBlockDocumentRequest]) (*connect.Response[intrav1.LoadLabelBlockDocumentResponse], error) {
	return respond(h.labels.LoadLabelBlockDocument(ctx, LoadLabelBlockDocumentQuery{
		LabelID: req.Msg.LabelId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	}))
}

func (h *InternalConnectHandler) ApplyLabelBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyLabelBlockBatchRequest]) (*connect.Response[intrav1.ApplyLabelBlockBatchResponse], error) {
	result, err := h.labels.ApplyLabelBlockBatch(ctx, ApplyLabelBlockBatchCommand{
		LabelID: req.Msg.LabelId, Locale: req.Msg.Locale, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Batch: req.Msg.Batch, AffectedLocaleValues: req.Msg.AffectedLocaleValues,
	})
	return respond(&intrav1.ApplyLabelBlockBatchResponse{
		DocumentRevision: result.Content.DocumentRevision.String(), Changed: result.Content.Changed,
		SourceChanged: result.Content.TranslationSourceChanged, ChangedLocales: result.Content.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}, err)
}

func (h *InternalConnectHandler) UpdateLabelLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateLabelLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateLabelLocaleMetadataResponse], error) {
	update, err := h.labels.UpdateLabelLocaleMetadata(ctx, UpdateLabelLocaleMetadataCommand{
		LabelID: req.Msg.LabelId, Locale: req.Msg.Locale, Title: req.Msg.Title, ExpectedRevision: req.Msg.ExpectedRevision,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	return respond(&intrav1.UpdateLabelLocaleMetadataResponse{
		DocumentRevision: update.Advance.DocumentRevision.String(), Changed: update.Advance.Changed,
		SourceChanged: update.Advance.TranslationSourceChanged, ChangedLocales: update.ChangedLocales,
		Locale: update.Locale,
	}, err)
}

func (h *InternalConnectHandler) UpdateLabelDocumentMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateLabelDocumentMetadataRequest]) (*connect.Response[intrav1.UpdateLabelDocumentMetadataResponse], error) {
	advanced, err := h.labels.UpdateLabelDocumentMetadata(ctx, UpdateLabelDocumentMetadataCommand{
		LabelID: req.Msg.LabelId, ExpectedRevision: req.Msg.ExpectedRevision, Update: req.Msg.Update,
		ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	return respond(&intrav1.UpdateLabelDocumentMetadataResponse{
		DocumentRevision: advanced.DocumentRevision.String(), Changed: advanced.Changed, SourceChanged: advanced.TranslationSourceChanged,
	}, err)
}

// logoVariant maps a theme asset variant to its Label logo slot; an
// unspecified variant is the light slot.
func logoVariant(variant managev1.ThemeAssetVariant) LogoVariant {
	switch variant {
	case managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_UNSPECIFIED, managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT:
		return LogoVariantLight
	case managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK:
		return LogoVariantDark
	default:
		return LogoVariantUnsupported
	}
}

// participantRole maps an API participant role to the stored role; an
// unknown role is empty, which the use case rejects.
func participantRole(role managev1.LabelParticipantRole) string {
	switch role {
	case managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER:
		return ParticipantRoleOwner
	case managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER:
		return ParticipantRoleManager
	default:
		return ""
	}
}
