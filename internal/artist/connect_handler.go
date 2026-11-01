package artist

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the manage Artist RPCs. Each method translates the
// request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedArtistServiceHandler
	artists *Service
}

// NewConnectHandler returns the manage Connect adapter of artists.
func NewConnectHandler(artists *Service) *ConnectHandler {
	return &ConnectHandler{artists: artists}
}

// InternalConnectHandler serves the collaboration service's Artist document
// RPCs.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalArtistServiceHandler
	artists *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of artists.
func NewInternalConnectHandler(artists *Service) *InternalConnectHandler {
	return &InternalConnectHandler{artists: artists}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// listArtistsQuery is the use-case query of a list request.
func listArtistsQuery(pagination *commonv1.PaginationRequest, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec) ListArtistsQuery {
	return ListArtistsQuery{Limit: pagination.GetLimit(), Offset: pagination.GetOffset(), Filters: filters, Sorts: sorts}
}

func (h *ConnectHandler) GetArtistEditorData(ctx context.Context, req *connect.Request[managev1.GetArtistEditorDataRequest]) (*connect.Response[managev1.GetArtistEditorDataResponse], error) {
	data, err := h.artists.GetArtistEditorData(ctx, GetArtistEditorDataQuery{ID: req.Msg.Id})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.GetArtistEditorDataResponse{
		Artist: artistMessage(data.Artist), LabelIds: data.LabelIDs, AllowedActions: artistActionMessages(data.AllowedActions), ImageRevision: data.ImageRevision,
	}, nil)
}

func (h *ConnectHandler) ListArtists(ctx context.Context, req *connect.Request[managev1.ListArtistsRequest]) (*connect.Response[managev1.ListArtistsResponse], error) {
	page, err := h.artists.ListArtists(ctx, listArtistsQuery(req.Msg.Pagination, req.Msg.Filters, req.Msg.Sorts))
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.ListArtistsResponse{Artists: ArtistMessages(page.Artists), Pagination: PaginationMessage(page.Pagination)}, nil)
}

func (h *ConnectHandler) ListArtistsAdmin(ctx context.Context, req *connect.Request[managev1.ListArtistsAdminRequest]) (*connect.Response[managev1.ListArtistsAdminResponse], error) {
	page, err := h.artists.ListArtistsAdmin(ctx, listArtistsQuery(req.Msg.Pagination, req.Msg.Filters, req.Msg.Sorts))
	if err != nil {
		return nil, errs.Wrap(err)
	}
	artists := make([]*managev1.ArtistWithStats, len(page.Artists))
	for i, item := range page.Artists {
		artists[i] = &managev1.ArtistWithStats{Artist: artistMessage(item.Artist), ReleaseCount: item.ReleaseCount}
	}
	return respond(&managev1.ListArtistsAdminResponse{Artists: artists, Pagination: PaginationMessage(page.Pagination)}, nil)
}

func (h *ConnectHandler) ListMyArtists(ctx context.Context, req *connect.Request[managev1.ListMyArtistsRequest]) (*connect.Response[managev1.ListMyArtistsResponse], error) {
	page, err := h.artists.ListMyArtists(ctx, listArtistsQuery(req.Msg.Pagination, req.Msg.Filters, req.Msg.Sorts))
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.ListMyArtistsResponse{Artists: ArtistMessages(page.Artists), Pagination: PaginationMessage(page.Pagination)}, nil)
}

func (h *ConnectHandler) CreateArtist(ctx context.Context, req *connect.Request[managev1.CreateArtistRequest]) (*connect.Response[managev1.Artist], error) {
	view, err := h.artists.CreateArtist(ctx, CreateArtistCommand{
		Name: req.Msg.Name, Slug: req.Msg.Slug, RealName: req.Msg.RealName, CountryCode: req.Msg.CountryCode,
		Website: req.Msg.Website, ParentArtistID: req.Msg.ParentArtistId, SocialLinks: req.Msg.SocialLinks,
		Document: req.Msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(artistMessage(view), nil)
}

func (h *ConnectHandler) DeleteArtist(ctx context.Context, req *connect.Request[managev1.DeleteArtistRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.artists.DeleteArtist(ctx, DeleteArtistCommand{ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) PreviewDeleteArtist(ctx context.Context, req *connect.Request[managev1.PreviewDeleteArtistRequest]) (*connect.Response[managev1.PreviewDeleteArtistResponse], error) {
	preview, err := h.artists.PreviewDeleteArtist(ctx, PreviewDeleteArtistQuery{ID: req.Msg.Id})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(deletionPreviewMessage(preview), nil)
}

func (h *ConnectHandler) PublishArtist(ctx context.Context, req *connect.Request[managev1.PublishArtistRequest]) (*connect.Response[managev1.ArtistLifecycleMutationResponse], error) {
	result, err := h.artists.PublishArtist(ctx, PublishArtistCommand{ID: req.Msg.Id})
	return respond(lifecycleMessage(result), err)
}

func (h *ConnectHandler) UnpublishArtist(ctx context.Context, req *connect.Request[managev1.UnpublishArtistRequest]) (*connect.Response[managev1.ArtistLifecycleMutationResponse], error) {
	result, err := h.artists.UnpublishArtist(ctx, UnpublishArtistCommand{ID: req.Msg.Id})
	return respond(lifecycleMessage(result), err)
}

func (h *ConnectHandler) SetArtistImage(ctx context.Context, req *connect.Request[managev1.SetArtistImageRequest]) (*connect.Response[managev1.SetArtistImageResponse], error) {
	update, err := h.artists.SetArtistImage(ctx, SetArtistImageCommand{ArtistID: req.Msg.ArtistId, FileID: req.Msg.FileId})
	response := &managev1.SetArtistImageResponse{OgGenerationRunId: update.OgGenerationRunID}
	if len(update.Images) > 0 {
		response.ImageAsset = update.Images[0].Asset
	}
	return respond(response, err)
}

func (h *ConnectHandler) DeleteArtistImage(ctx context.Context, req *connect.Request[managev1.DeleteArtistImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	update, err := h.artists.DeleteArtistImage(ctx, DeleteArtistImageCommand{ArtistID: req.Msg.ArtistId})
	return respond(&managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: update.OgGenerationRunID}, err)
}

func (h *ConnectHandler) SetArtistImages(ctx context.Context, req *connect.Request[managev1.SetArtistImagesRequest]) (*connect.Response[managev1.SetArtistImagesResponse], error) {
	update, err := h.artists.SetArtistImages(ctx, SetArtistImagesCommand{
		ArtistID: req.Msg.ArtistId, FileIDs: req.Msg.FileIds, ExpectedRevision: req.Msg.ExpectedRevision,
	})
	return respond(&managev1.SetArtistImagesResponse{
		Images:   append(make([]*managev1.ArtistImage, 0, len(update.Images)), artistImageMessages(update.Images)...),
		Revision: artistImageRevision(update.Images), OgGenerationRunId: update.OgGenerationRunID,
	}, err)
}

func (h *ConnectHandler) ListArtistParticipants(ctx context.Context, req *connect.Request[managev1.ListArtistParticipantsRequest]) (*connect.Response[managev1.ListArtistParticipantsResponse], error) {
	participants, err := h.artists.ListArtistParticipants(ctx, ListArtistParticipantsQuery{ArtistID: req.Msg.ArtistId})
	return respond(&managev1.ListArtistParticipantsResponse{Participants: entityparticipant.MapViews(participants, artistParticipantMessage)}, err)
}

func (h *ConnectHandler) SetArtistParticipant(ctx context.Context, req *connect.Request[managev1.SetArtistParticipantRequest]) (*connect.Response[managev1.ArtistParticipant], error) {
	view, err := h.artists.SetArtistParticipant(ctx, SetArtistParticipantCommand{
		ArtistID: req.Msg.ArtistId, MemberID: req.Msg.MemberId, Role: participantRole(req.Msg.Role),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(artistParticipantMessage(view), nil)
}

func (h *ConnectHandler) RemoveArtistParticipant(ctx context.Context, req *connect.Request[managev1.RemoveArtistParticipantRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.artists.RemoveArtistParticipant(ctx, RemoveArtistParticipantCommand{ArtistID: req.Msg.ArtistId, MemberID: req.Msg.MemberId})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) CheckArtistSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckArtistSlugAvailableRequest]) (*connect.Response[managev1.CheckArtistSlugAvailableResponse], error) {
	available, err := h.artists.CheckArtistSlugAvailable(ctx, CheckArtistSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeArtistID: req.Msg.ExcludeArtistId})
	return respond(&managev1.CheckArtistSlugAvailableResponse{Available: available}, err)
}

func (h *InternalConnectHandler) LoadArtistBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadArtistBlockDocumentRequest]) (*connect.Response[intrav1.LoadArtistBlockDocumentResponse], error) {
	room, err := h.artists.LoadArtistBlockDocument(ctx, LoadArtistBlockDocumentQuery{
		ArtistID: req.Msg.ArtistId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(roomDocumentMessage(room), nil)
}

func (h *InternalConnectHandler) ApplyArtistBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyArtistBlockBatchRequest]) (*connect.Response[intrav1.ApplyArtistBlockBatchResponse], error) {
	result, err := h.artists.ApplyArtistBlockBatch(ctx, ApplyArtistBlockBatchCommand{
		ArtistID: req.Msg.ArtistId, Locale: req.Msg.Locale, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Batch: req.Msg.Batch, AffectedLocaleValues: req.Msg.AffectedLocaleValues,
	})
	return respond(&intrav1.ApplyArtistBlockBatchResponse{
		DocumentRevision: result.Content.DocumentRevision.String(), Changed: result.Content.Changed,
		SourceChanged:  result.TargetRevision == nil && result.Content.TranslationSourceChanged,
		ChangedLocales: result.Content.ChangedLocales, Locale: result.Locale, TargetRevision: result.TargetRevision,
	}, err)
}

func (h *InternalConnectHandler) UpdateArtistLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateArtistLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateArtistLocaleMetadataResponse], error) {
	update, err := h.artists.UpdateArtistLocaleMetadata(ctx, UpdateArtistLocaleMetadataCommand{
		ArtistID: req.Msg.ArtistId, Locale: req.Msg.Locale, Title: req.Msg.Title, ExpectedRevision: req.Msg.ExpectedRevision,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	return respond(&intrav1.UpdateArtistLocaleMetadataResponse{
		DocumentRevision: update.Result.DocumentRevision.String(), Changed: update.Result.Changed,
		SourceChanged: update.SourceChanged, ChangedLocales: update.Result.ChangedLocales,
		Locale: update.Locale, TargetRevision: update.TargetRevision,
	}, err)
}

func (h *InternalConnectHandler) UpdateArtistDocumentMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateArtistDocumentMetadataRequest]) (*connect.Response[intrav1.UpdateArtistDocumentMetadataResponse], error) {
	update, err := h.artists.UpdateArtistDocumentMetadata(ctx, UpdateArtistDocumentMetadataCommand{
		ArtistID: req.Msg.ArtistId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		Update: documentMetadataUpdate(req.Msg.Update), ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	return respond(&intrav1.UpdateArtistDocumentMetadataResponse{
		DocumentRevision: update.Advance.DocumentRevision.String(), Changed: update.Advance.Changed,
		SourceChanged: update.Advance.TranslationSourceChanged, Locale: update.Locale,
	}, err)
}

// participantRole maps an API participant role to the stored role; an
// unknown role is empty, which the use case rejects.
func participantRole(role managev1.ArtistParticipantRole) string {
	switch role {
	case managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_OWNER:
		return ParticipantRoleOwner
	case managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER:
		return ParticipantRoleManager
	default:
		return ""
	}
}

// artistMessage renders an Artist view.
func artistMessage(view View) *managev1.Artist {
	a := view.Artist
	artist := &managev1.Artist{
		Id:           a.ID,
		SourceLocale: a.SourceLocale,
		Name:         view.Name,
		Document:     view.Document,
		Status:       string(a.Status),
		CreatedAt:    timestamppb.New(a.CreatedAt),
		OgAsset:      view.OgImage,
		Revision:     view.Revision,
	}
	if a.Slug != nil {
		artist.Slug = a.Slug
	}
	if a.RealName != nil {
		artist.RealName = a.RealName
	}
	if a.ParentArtistID != nil {
		artist.ParentArtistId = a.ParentArtistID
	}
	if a.CountryCode != nil {
		artist.CountryCode = a.CountryCode
	}
	if a.Website != nil {
		artist.Website = a.Website
	}
	if a.PublishedAt != nil {
		artist.PublishedAt = timestamppb.New(*a.PublishedAt)
	}
	if a.UpdatedAt != nil {
		artist.UpdatedAt = timestamppb.New(*a.UpdatedAt)
	}
	if a.SocialLinks != nil {
		artist.SocialLinks = a.SocialLinks
	}
	artist.Images = artistImageMessages(view.Images)
	if len(view.Images) > 0 {
		artist.ImageAsset = view.Images[0].Asset
	}
	return artist
}

// ArtistMessages renders a page of Artists, for the manage API and its MCP
// tools.
func ArtistMessages(views []View) []*managev1.Artist {
	artists := make([]*managev1.Artist, len(views))
	for i, view := range views {
		artists[i] = artistMessage(view)
	}
	return artists
}

// PaginationMessage renders a page position, for the manage API and its
// MCP tools.
func PaginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

// artistImageMessages renders the ordered images; the first is the primary
// image.
func artistImageMessages(images []ArtistImageProjection) []*managev1.ArtistImage {
	messages := make([]*managev1.ArtistImage, 0, len(images))
	for index, image := range images {
		messages = append(messages, &managev1.ArtistImage{
			FileId:    image.FileID,
			Asset:     image.Asset,
			SortOrder: int32(index),
			Primary:   index == 0,
		})
	}
	return messages
}

func artistActionMessages(actions []Action) []managev1.ArtistAction {
	messages := make([]managev1.ArtistAction, 0, len(actions))
	for _, action := range actions {
		messages = append(messages, artistActionMessage(action))
	}
	return messages
}

func artistActionMessage(action Action) managev1.ArtistAction {
	switch action {
	case ActionEdit:
		return managev1.ArtistAction_ARTIST_ACTION_EDIT
	case ActionManageShareLinks:
		return managev1.ArtistAction_ARTIST_ACTION_MANAGE_SHARE_LINKS
	case ActionDelete:
		return managev1.ArtistAction_ARTIST_ACTION_DELETE
	case ActionManageParticipants:
		return managev1.ArtistAction_ARTIST_ACTION_MANAGE_PARTICIPANTS
	case ActionPublish:
		return managev1.ArtistAction_ARTIST_ACTION_PUBLISH
	case ActionUnpublish:
		return managev1.ArtistAction_ARTIST_ACTION_UNPUBLISH
	case ActionRemoveOwner:
		return managev1.ArtistAction_ARTIST_ACTION_REMOVE_OWNER
	default:
		return managev1.ArtistAction_ARTIST_ACTION_UNSPECIFIED
	}
}

func deletionPreviewMessage(preview DeletionPreview) *managev1.PreviewDeleteArtistResponse {
	response := &managev1.PreviewDeleteArtistResponse{
		Revision: preview.Revision,
		Impacts:  make([]*managev1.ArtistDeletionImpact, 0, len(preview.Impacts)),
	}
	for _, impact := range preview.Impacts {
		response.TotalRelationCount += impact.RelationCount
		response.Impacts = append(response.Impacts, &managev1.ArtistDeletionImpact{
			Domain:        managev1.ArtistRelationDomain(impact.Domain),
			EntityId:      impact.EntityID,
			Label:         impact.Label,
			RelationCount: impact.RelationCount,
		})
	}
	return response
}

func lifecycleMessage(result LifecycleResult) *managev1.ArtistLifecycleMutationResponse {
	response := &managev1.ArtistLifecycleMutationResponse{
		Id: result.ID, Changed: result.Changed, Status: artistStatusToProto(result.Status),
	}
	if result.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*result.PublishedAt)
	}
	if result.UpdatedAt != nil {
		response.UpdatedAt = timestamppb.New(*result.UpdatedAt)
	}
	return response
}

// artistStatusToProto maps a stored Artist status to the API enum. A value
// the API does not know is UNSPECIFIED.
func artistStatusToProto(status Status) managev1.ArtistStatus {
	switch status {
	case StatusDraft:
		return managev1.ArtistStatus_ARTIST_STATUS_DRAFT
	case StatusPublished:
		return managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED
	default:
		return managev1.ArtistStatus_ARTIST_STATUS_UNSPECIFIED
	}
}

func artistParticipantMessage(view entityparticipant.View) *managev1.ArtistParticipant {
	role := managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER
	if view.Role == ParticipantRoleOwner {
		role = managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_OWNER
	}
	return &managev1.ArtistParticipant{
		Member: view.Member, Role: role, CreatedAt: timestamppb.New(view.CreatedAt),
		HasEffectiveAuthority: view.HasEffectiveAuthority,
	}
}

func roomDocumentMessage(room RoomDocument) *intrav1.LoadArtistBlockDocumentResponse {
	metadata := room.Metadata
	response := &intrav1.LoadArtistBlockDocumentResponse{
		Document: room.Document, DocumentRevision: room.DocumentRevision,
		SourceMetadata: &intrav1.ArtistLocaleMetadata{Locale: room.SourceMetadata.Locale, Title: room.SourceMetadata.Title},
		Metadata: &intrav1.ArtistDocumentMetadata{
			RealName: metadata.RealName, CountryCode: metadata.CountryCode, Website: metadata.Website,
			SocialLinks: metadata.SocialLinks, Slug: metadata.Slug, LabelIds: metadata.LabelIDs,
			ParentArtistId: metadata.ParentArtistID,
		},
		Locale: room.Locale, LocaleExists: room.LocaleExists, PresentLocaleValues: room.PresentLocaleValues,
		TargetRevision: room.TargetRevision,
	}
	if room.LocaleMetadata != nil {
		response.LocaleMetadata = &intrav1.ArtistLocaleMetadata{Locale: room.LocaleMetadata.Locale, Title: room.LocaleMetadata.Title}
	}
	return response
}

// documentMetadataUpdate maps a room's metadata update; nil stays nil, which
// the use case rejects.
func documentMetadataUpdate(update *intrav1.ArtistDocumentMetadataUpdate) *DocumentMetadataUpdate {
	if update == nil {
		return nil
	}
	return &DocumentMetadataUpdate{
		RealName: stringMutation(update.RealName), CountryCode: stringMutation(update.CountryCode),
		Website: stringMutation(update.Website), Slug: stringMutation(update.Slug),
		ParentArtistID:     stringMutation(update.ParentArtistId),
		SocialLinksPresent: update.SocialLinks != nil, SocialLinks: update.SocialLinks.GetValues(),
		LabelIDsPresent: update.LabelIds != nil, LabelIDs: update.LabelIds.GetValues(),
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
