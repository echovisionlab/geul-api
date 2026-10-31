package artist

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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
	return respond(&managev1.GetArtistEditorDataResponse{
		Artist: data.Artist, LabelIds: data.LabelIDs, AllowedActions: data.AllowedActions, ImageRevision: data.ImageRevision,
	}, err)
}

func (h *ConnectHandler) ListArtists(ctx context.Context, req *connect.Request[managev1.ListArtistsRequest]) (*connect.Response[managev1.ListArtistsResponse], error) {
	page, err := h.artists.ListArtists(ctx, listArtistsQuery(req.Msg.Pagination, req.Msg.Filters, req.Msg.Sorts))
	return respond(&managev1.ListArtistsResponse{Artists: page.Artists, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) ListArtistsAdmin(ctx context.Context, req *connect.Request[managev1.ListArtistsAdminRequest]) (*connect.Response[managev1.ListArtistsAdminResponse], error) {
	page, err := h.artists.ListArtistsAdmin(ctx, listArtistsQuery(req.Msg.Pagination, req.Msg.Filters, req.Msg.Sorts))
	return respond(&managev1.ListArtistsAdminResponse{Artists: page.Artists, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) ListMyArtists(ctx context.Context, req *connect.Request[managev1.ListMyArtistsRequest]) (*connect.Response[managev1.ListMyArtistsResponse], error) {
	page, err := h.artists.ListMyArtists(ctx, listArtistsQuery(req.Msg.Pagination, req.Msg.Filters, req.Msg.Sorts))
	return respond(&managev1.ListMyArtistsResponse{Artists: page.Artists, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) CreateArtist(ctx context.Context, req *connect.Request[managev1.CreateArtistRequest]) (*connect.Response[managev1.Artist], error) {
	return respond(h.artists.CreateArtist(ctx, CreateArtistCommand{
		Name: req.Msg.Name, Slug: req.Msg.Slug, RealName: req.Msg.RealName, CountryCode: req.Msg.CountryCode,
		Website: req.Msg.Website, ParentArtistID: req.Msg.ParentArtistId, SocialLinks: req.Msg.SocialLinks,
		Document: req.Msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) DeleteArtist(ctx context.Context, req *connect.Request[managev1.DeleteArtistRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.artists.DeleteArtist(ctx, DeleteArtistCommand{ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) PreviewDeleteArtist(ctx context.Context, req *connect.Request[managev1.PreviewDeleteArtistRequest]) (*connect.Response[managev1.PreviewDeleteArtistResponse], error) {
	return respond(h.artists.PreviewDeleteArtist(ctx, PreviewDeleteArtistQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishArtist(ctx context.Context, req *connect.Request[managev1.PublishArtistRequest]) (*connect.Response[managev1.ArtistLifecycleMutationResponse], error) {
	return respond(h.artists.PublishArtist(ctx, PublishArtistCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) UnpublishArtist(ctx context.Context, req *connect.Request[managev1.UnpublishArtistRequest]) (*connect.Response[managev1.ArtistLifecycleMutationResponse], error) {
	return respond(h.artists.UnpublishArtist(ctx, UnpublishArtistCommand{ID: req.Msg.Id}))
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
	artist := &managev1.Artist{}
	applyManageArtistImages(artist, update.Images)
	return respond(&managev1.SetArtistImagesResponse{
		Images:   append(make([]*managev1.ArtistImage, 0, len(update.Images)), artist.Images...),
		Revision: artistImageRevision(update.Images), OgGenerationRunId: update.OgGenerationRunID,
	}, err)
}

func (h *ConnectHandler) ListArtistParticipants(ctx context.Context, req *connect.Request[managev1.ListArtistParticipantsRequest]) (*connect.Response[managev1.ListArtistParticipantsResponse], error) {
	participants, err := h.artists.ListArtistParticipants(ctx, ListArtistParticipantsQuery{ArtistID: req.Msg.ArtistId})
	return respond(&managev1.ListArtistParticipantsResponse{Participants: participants}, err)
}

func (h *ConnectHandler) SetArtistParticipant(ctx context.Context, req *connect.Request[managev1.SetArtistParticipantRequest]) (*connect.Response[managev1.ArtistParticipant], error) {
	return respond(h.artists.SetArtistParticipant(ctx, SetArtistParticipantCommand{
		ArtistID: req.Msg.ArtistId, MemberID: req.Msg.MemberId, Role: participantRole(req.Msg.Role),
	}))
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
	return respond(h.artists.LoadArtistBlockDocument(ctx, LoadArtistBlockDocumentQuery{
		ArtistID: req.Msg.ArtistId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	}))
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
		Update: req.Msg.Update, ContributorMemberIDs: req.Msg.ContributorMemberIds,
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
