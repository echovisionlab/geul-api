package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/memberref"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the manage Label RPCs. Each method translates the
// request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	labels *Service
}

// NewConnectHandler returns the manage Connect adapter of labels.
func NewConnectHandler(labels *Service) *ConnectHandler {
	return &ConnectHandler{labels: labels}
}

// InternalConnectHandler serves the collaboration service's Label document
// RPCs.
type InternalConnectHandler struct {
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
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.GetLabelEditorDataResponse{Label: labelMessage(data.Label), AllowedActions: labelActionMessages(data.AllowedActions)}, nil)
}

func (h *ConnectHandler) ListLabels(ctx context.Context, req *connect.Request[managev1.ListLabelsRequest]) (*connect.Response[managev1.ListLabelsResponse], error) {
	page, err := h.labels.ListLabels(ctx, ListLabelsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	labels := make([]*managev1.Label, len(page.Labels))
	for i, view := range page.Labels {
		labels[i] = labelMessage(view)
	}
	return respond(&managev1.ListLabelsResponse{Labels: labels, Pagination: paginationMessage(page.Pagination)}, nil)
}

func (h *ConnectHandler) ListLabelsAdmin(ctx context.Context, req *connect.Request[managev1.ListLabelsAdminRequest]) (*connect.Response[managev1.ListLabelsAdminResponse], error) {
	page, err := h.labels.ListLabelsAdmin(ctx, ListLabelsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.ListLabelsAdminResponse{Labels: labelsWithStatsMessages(page.Labels), Pagination: paginationMessage(page.Pagination)}, nil)
}

func (h *ConnectHandler) CreateLabel(ctx context.Context, req *connect.Request[managev1.CreateLabelRequest]) (*connect.Response[managev1.Label], error) {
	view, err := h.labels.CreateLabel(ctx, CreateLabelCommand{
		Name: req.Msg.Name, Slug: req.Msg.Slug, CountryCode: req.Msg.CountryCode, Website: req.Msg.Website,
		ParentLabelID: req.Msg.ParentLabelId, SocialLinks: req.Msg.SocialLinks, Document: req.Msg.Document,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(labelMessage(view), nil)
}

func (h *ConnectHandler) DeleteLabel(ctx context.Context, req *connect.Request[managev1.DeleteLabelRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.labels.DeleteLabel(ctx, DeleteLabelCommand{ID: req.Msg.Id, PreviewRevision: req.Msg.PreviewRevision})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) PreviewDeleteLabel(ctx context.Context, req *connect.Request[managev1.PreviewDeleteLabelRequest]) (*connect.Response[managev1.PreviewDeleteLabelResponse], error) {
	preview, err := h.labels.PreviewDeleteLabel(ctx, PreviewDeleteLabelQuery{ID: req.Msg.Id})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(deletionPreviewMessage(preview), nil)
}

func (h *ConnectHandler) PublishLabel(ctx context.Context, req *connect.Request[managev1.PublishLabelRequest]) (*connect.Response[managev1.LabelLifecycleMutationResponse], error) {
	result, err := h.labels.PublishLabel(ctx, PublishLabelCommand{ID: req.Msg.Id})
	return respond(lifecycleMessage(result), err)
}

func (h *ConnectHandler) UnpublishLabel(ctx context.Context, req *connect.Request[managev1.UnpublishLabelRequest]) (*connect.Response[managev1.LabelLifecycleMutationResponse], error) {
	result, err := h.labels.UnpublishLabel(ctx, UnpublishLabelCommand{ID: req.Msg.Id})
	return respond(lifecycleMessage(result), err)
}

func (h *ConnectHandler) SetLabelImage(ctx context.Context, req *connect.Request[managev1.SetLabelImageRequest]) (*connect.Response[managev1.SetLabelImageResponse], error) {
	update, err := h.labels.SetLabelImage(ctx, SetLabelImageCommand{LabelID: req.Msg.LabelId, FileID: req.Msg.FileId, Variant: logoVariant(req.Msg.Variant)})
	return respond(&managev1.SetLabelImageResponse{
		ImageLightAsset: mediaasset.ReferenceProto(update.Light), ImageDarkAsset: mediaasset.ReferenceProto(update.Dark), OgGenerationRunId: update.OgGenerationRunID,
	}, err)
}

func (h *ConnectHandler) DeleteLabelImage(ctx context.Context, req *connect.Request[managev1.DeleteLabelImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	runID, err := h.labels.DeleteLabelImage(ctx, DeleteLabelImageCommand{LabelID: req.Msg.LabelId, Variant: logoVariant(req.Msg.Variant)})
	return respond(&managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: runID}, err)
}

func (h *ConnectHandler) ListLabelParticipants(ctx context.Context, req *connect.Request[managev1.ListLabelParticipantsRequest]) (*connect.Response[managev1.ListLabelParticipantsResponse], error) {
	participants, err := h.labels.ListLabelParticipants(ctx, ListLabelParticipantsQuery{LabelID: req.Msg.LabelId})
	return respond(&managev1.ListLabelParticipantsResponse{Participants: entityparticipant.MapViews(participants, labelParticipantMessage)}, err)
}

func (h *ConnectHandler) SetLabelParticipant(ctx context.Context, req *connect.Request[managev1.SetLabelParticipantRequest]) (*connect.Response[managev1.LabelParticipant], error) {
	view, err := h.labels.SetLabelParticipant(ctx, SetLabelParticipantCommand{
		LabelID: req.Msg.LabelId, MemberID: req.Msg.MemberId, Role: participantRole(req.Msg.Role),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(labelParticipantMessage(view), nil)
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
	room, err := h.labels.LoadLabelBlockDocument(ctx, LoadLabelBlockDocumentQuery{
		LabelID: req.Msg.LabelId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(roomDocumentMessage(room), nil)
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
		LabelID: req.Msg.LabelId, ExpectedRevision: req.Msg.ExpectedRevision, Update: documentMetadataUpdate(req.Msg.Update),
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

// labelMessage renders a Label view.
func labelMessage(view View) *managev1.Label {
	l := view.Label
	label := &managev1.Label{
		Id:           l.ID,
		Name:         view.Name,
		Document:     view.Document,
		Status:       string(l.Status),
		CreatedAt:    timestamppb.New(l.CreatedAt),
		UpdatedAt:    timestamppb.New(l.UpdatedAt),
		OgAsset:      mediaasset.ReferenceProto(view.OgImage),
		Revision:     view.Revision,
		SourceLocale: l.SourceLocale,
	}
	if l.Slug != nil {
		label.Slug = l.Slug
	}
	if l.CountryCode != nil {
		label.CountryCode = l.CountryCode
	}
	if l.Website != nil {
		label.Website = l.Website
	}
	label.ImageLightAsset = mediaasset.ReferenceProto(view.LightLogo)
	label.ImageDarkAsset = mediaasset.ReferenceProto(view.DarkLogo)
	if l.ParentLabelID != nil {
		label.ParentLabelId = l.ParentLabelID
	}
	if l.SocialLinks != nil {
		label.SocialLinks = l.SocialLinks
	}
	if l.PublishedAt != nil {
		label.PublishedAt = timestamppb.New(*l.PublishedAt)
	}
	return label
}

// labelsWithStatsMessages renders an administrator page of Labels.
func labelsWithStatsMessages(views []AdminView) []*managev1.LabelWithStats {
	labels := make([]*managev1.LabelWithStats, len(views))
	for i, view := range views {
		labels[i] = &managev1.LabelWithStats{
			Label: labelMessage(view.Label), ArtistCount: view.ArtistCount, ReleaseCount: view.ReleaseCount,
		}
	}
	return labels
}

// PaginationMessage renders a page position, for the manage API and its
// MCP tools.
func PaginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return paginationMessage(pagination)
}

func paginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

func labelActionMessages(actions []Action) []managev1.LabelAction {
	messages := make([]managev1.LabelAction, 0, len(actions))
	for _, action := range actions {
		messages = append(messages, labelActionMessage(action))
	}
	return messages
}

func labelActionMessage(action Action) managev1.LabelAction {
	switch action {
	case ActionEdit:
		return managev1.LabelAction_LABEL_ACTION_EDIT
	case ActionManageShareLinks:
		return managev1.LabelAction_LABEL_ACTION_MANAGE_SHARE_LINKS
	case ActionDelete:
		return managev1.LabelAction_LABEL_ACTION_DELETE
	case ActionManageParticipants:
		return managev1.LabelAction_LABEL_ACTION_MANAGE_PARTICIPANTS
	case ActionPublish:
		return managev1.LabelAction_LABEL_ACTION_PUBLISH
	case ActionUnpublish:
		return managev1.LabelAction_LABEL_ACTION_UNPUBLISH
	case ActionRemoveOwner:
		return managev1.LabelAction_LABEL_ACTION_REMOVE_OWNER
	default:
		return managev1.LabelAction_LABEL_ACTION_UNSPECIFIED
	}
}

func deletionPreviewMessage(preview DeletionPreview) *managev1.PreviewDeleteLabelResponse {
	response := &managev1.PreviewDeleteLabelResponse{Revision: preview.Revision}
	for _, impact := range preview.Impacts {
		kind := managev1.LabelDeletionImpactKind(impact.Kind)
		response.Impacts = append(response.Impacts, &managev1.LabelDeletionImpact{
			Kind: kind, EntityId: impact.EntityID, DisplayName: impact.DisplayName,
		})
		switch kind {
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_CHILD_LABEL:
			response.ChildLabelCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_ARTIST:
			response.ArtistCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_PROGRAM_EVENT:
			response.ProgramEventCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_RELEASE:
			response.ReleaseCount++
		}
	}
	return response
}

func lifecycleMessage(result LifecycleResult) *managev1.LabelLifecycleMutationResponse {
	response := &managev1.LabelLifecycleMutationResponse{
		Id: result.ID, Changed: result.Changed, Status: labelStatusToProto(result.Status),
		UpdatedAt: timestamppb.New(result.UpdatedAt),
	}
	if result.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*result.PublishedAt)
	}
	return response
}

// labelStatusToProto maps a stored Label status to the API enum. A value the
// API does not know is UNSPECIFIED.
func labelStatusToProto(status Status) managev1.LabelStatus {
	switch status {
	case StatusDraft:
		return managev1.LabelStatus_LABEL_STATUS_DRAFT
	case StatusPublished:
		return managev1.LabelStatus_LABEL_STATUS_PUBLISHED
	default:
		return managev1.LabelStatus_LABEL_STATUS_UNSPECIFIED
	}
}

func labelParticipantMessage(view entityparticipant.View) *managev1.LabelParticipant {
	role := managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER
	if view.Role == ParticipantRoleOwner {
		role = managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER
	}
	return &managev1.LabelParticipant{
		Member: memberref.ReferenceMessage(view.Member), Role: role, CreatedAt: timestamppb.New(view.CreatedAt),
		HasEffectiveAuthority: view.HasEffectiveAuthority,
	}
}

func roomDocumentMessage(room RoomDocument) *intrav1.LoadLabelBlockDocumentResponse {
	response := &intrav1.LoadLabelBlockDocumentResponse{
		Document: room.Document, DocumentRevision: room.DocumentRevision,
		SourceMetadata: &intrav1.LabelLocaleMetadata{Locale: room.SourceMetadata.Locale, Title: room.SourceMetadata.Title},
		Metadata: &intrav1.LabelDocumentMetadata{
			Slug: room.Metadata.Slug, CountryCode: room.Metadata.CountryCode, Website: room.Metadata.Website,
			SocialLinks: room.Metadata.SocialLinks, ParentLabelId: room.Metadata.ParentLabelID,
		},
		Locale: room.Locale, LocaleExists: room.LocaleExists, TargetRevision: room.TargetRevision,
		PresentLocaleValues: room.PresentLocaleValues,
	}
	if room.LocaleMetadata != nil {
		response.LocaleMetadata = &intrav1.LabelLocaleMetadata{Locale: room.LocaleMetadata.Locale, Title: room.LocaleMetadata.Title}
	}
	return response
}

// documentMetadataUpdate maps a room's metadata update; nil stays nil, which
// the use case rejects.
func documentMetadataUpdate(update *intrav1.LabelDocumentMetadataUpdate) *DocumentMetadataUpdate {
	if update == nil {
		return nil
	}
	return &DocumentMetadataUpdate{
		Slug: stringMutation(update.Slug), CountryCode: stringMutation(update.CountryCode),
		Website: stringMutation(update.Website), ParentLabelID: stringMutation(update.ParentLabelId),
		SocialLinksPresent: update.SocialLinks != nil, SocialLinks: update.SocialLinks.GetValues(),
	}
}

func stringMutation(input *intrav1.NullableStringMutation) *entityparticipant.StringMutation {
	if input == nil {
		return nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		return &entityparticipant.StringMutation{Set: &operation.Set}
	case *intrav1.NullableStringMutation_Clear:
		return &entityparticipant.StringMutation{Clear: &operation.Clear}
	default:
		return &entityparticipant.StringMutation{}
	}
}
