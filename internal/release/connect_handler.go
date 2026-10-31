package release

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the administrator Release RPCs. Each method
// translates the request into a use-case command or query, and the result
// into the response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedReleaseServiceHandler
	releases *Service
}

// NewConnectHandler returns the administrator Release Connect adapter.
func NewConnectHandler(releases *Service) *ConnectHandler {
	return &ConnectHandler{releases: releases}
}

// TrackConnectHandler serves the administrator Track RPCs.
type TrackConnectHandler struct {
	managev1connect.UnimplementedTrackServiceHandler
	releases *Service
}

// NewTrackConnectHandler returns the administrator Track Connect adapter.
func NewTrackConnectHandler(releases *Service) *TrackConnectHandler {
	return &TrackConnectHandler{releases: releases}
}

// InternalConnectHandler serves the collaboration service's typed Release
// Block boundary.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalReleaseServiceHandler
	releases *Service
}

// NewInternalConnectHandler returns the collaboration Release Connect adapter.
func NewInternalConnectHandler(releases *Service) *InternalConnectHandler {
	return &InternalConnectHandler{releases: releases}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) GetRelease(ctx context.Context, req *connect.Request[managev1.GetReleaseRequest]) (*connect.Response[managev1.Release], error) {
	return respond(h.releases.GetRelease(ctx, GetReleaseQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) GetReleaseRelations(ctx context.Context, req *connect.Request[managev1.GetReleaseRelationsRequest]) (*connect.Response[managev1.GetReleaseRelationsResponse], error) {
	return respond(h.releases.GetReleaseRelations(ctx, GetReleaseRelationsQuery{ReleaseID: req.Msg.ReleaseId}))
}

func (h *ConnectHandler) ListReleasesAdmin(ctx context.Context, req *connect.Request[managev1.ListReleasesAdminRequest]) (*connect.Response[managev1.ListReleasesAdminResponse], error) {
	page, err := h.releases.ListReleasesAdmin(ctx, ListReleasesQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
		Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&managev1.ListReleasesAdminResponse{
		Releases: page.Releases,
		Pagination: &commonv1.PaginationResponse{
			Total: int32(page.Total), Limit: page.Limit, Offset: page.Offset,
			HasMore: page.Offset+page.Limit < int32(page.Total),
		},
	}, err)
}

func (h *ConnectHandler) CreateRelease(ctx context.Context, req *connect.Request[managev1.CreateReleaseRequest]) (*connect.Response[managev1.Release], error) {
	return respond(h.releases.CreateRelease(ctx, CreateReleaseCommand{
		Title: req.Msg.Title, SourceLocale: req.Msg.SourceLocale, AcceptLanguage: req.Header().Get("Accept-Language"),
		Type: releaseTypeFromProto(req.Msg.Type), Slug: req.Msg.Slug, ReleaseDate: optionalTime(req.Msg.ReleaseDate),
		SpotifyURL: req.Msg.SpotifyUrl, AppleMusicURL: req.Msg.AppleMusicUrl, BandcampURL: req.Msg.BandcampUrl,
		YoutubeMusicURL: req.Msg.YoutubeMusicUrl, CatalogNumber: req.Msg.CatalogNumber, Document: req.Msg.Document,
	}))
}

func (h *ConnectHandler) UpdateRelease(ctx context.Context, req *connect.Request[managev1.UpdateReleaseRequest]) (*connect.Response[managev1.UpdateReleaseResponse], error) {
	command := UpdateReleaseCommand{
		ID: req.Msg.Id, Slug: req.Msg.Slug, CatalogNumber: req.Msg.CatalogNumber,
		SpotifyURL: req.Msg.SpotifyUrl, AppleMusicURL: req.Msg.AppleMusicUrl,
		BandcampURL: req.Msg.BandcampUrl, YoutubeMusicURL: req.Msg.YoutubeMusicUrl,
	}
	if req.Msg.Type != nil {
		releaseType := releaseTypeFromProto(*req.Msg.Type)
		command.Type = &releaseType
	}
	switch change := req.Msg.ReleaseDateChange.(type) {
	case *managev1.UpdateReleaseRequest_SetReleaseDate:
		command.ReleaseDateChange = ReleaseDateSet
		if change.SetReleaseDate.IsValid() {
			command.ReleaseDate = optionalTime(change.SetReleaseDate)
		}
	case *managev1.UpdateReleaseRequest_ClearReleaseDate:
		command.ReleaseDateChange = ReleaseDateCleared
	}
	update, err := h.releases.UpdateRelease(ctx, command)
	return respond(&managev1.UpdateReleaseResponse{
		Id: update.ID, Changed: update.Changed,
		UpdatedAt: timestamppb.New(update.UpdatedAt), ReleaseDate: timestampProtoPtr(update.ReleaseDate),
	}, err)
}

func (h *ConnectHandler) DeleteRelease(ctx context.Context, req *connect.Request[managev1.DeleteReleaseRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.releases.DeleteRelease(ctx, DeleteReleaseCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) PublishRelease(ctx context.Context, req *connect.Request[managev1.PublishReleaseRequest]) (*connect.Response[managev1.ReleaseLifecycleMutationResponse], error) {
	change, err := h.releases.PublishRelease(ctx, PublishReleaseCommand{ID: req.Msg.Id})
	return respond(lifecycleMessage(change), err)
}

func (h *ConnectHandler) UnpublishRelease(ctx context.Context, req *connect.Request[managev1.UnpublishReleaseRequest]) (*connect.Response[managev1.ReleaseLifecycleMutationResponse], error) {
	change, err := h.releases.UnpublishRelease(ctx, UnpublishReleaseCommand{ID: req.Msg.Id})
	return respond(lifecycleMessage(change), err)
}

func lifecycleMessage(change LifecycleChange) *managev1.ReleaseLifecycleMutationResponse {
	return &managev1.ReleaseLifecycleMutationResponse{
		Id: change.ID, Changed: change.Changed, Status: releaseStatusToProto(change.Status),
		PublishedAt: timestampProtoPtr(change.PublishedAt), UpdatedAt: timestamppb.New(change.UpdatedAt),
	}
}

func (h *ConnectHandler) SetReleaseArtwork(ctx context.Context, req *connect.Request[managev1.SetReleaseArtworkRequest]) (*connect.Response[managev1.SetReleaseArtworkResponse], error) {
	asset, err := h.releases.SetReleaseArtwork(ctx, SetReleaseArtworkCommand{ReleaseID: req.Msg.ReleaseId, FileID: req.Msg.FileId})
	return respond(&managev1.SetReleaseArtworkResponse{ArtworkAsset: asset}, err)
}

func (h *ConnectHandler) DeleteReleaseArtwork(ctx context.Context, req *connect.Request[managev1.DeleteReleaseArtworkRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	err := h.releases.DeleteReleaseArtwork(ctx, DeleteReleaseArtworkCommand{ReleaseID: req.Msg.ReleaseId})
	return respond(&managev1.OgAssetDeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) CheckReleaseSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckReleaseSlugAvailableRequest]) (*connect.Response[managev1.CheckReleaseSlugAvailableResponse], error) {
	available, err := h.releases.CheckReleaseSlugAvailable(ctx, CheckReleaseSlugQuery{Slug: req.Msg.Slug, ExcludeReleaseID: req.Msg.ExcludeReleaseId})
	return respond(&managev1.CheckReleaseSlugAvailableResponse{Available: available}, err)
}

func (h *ConnectHandler) SetReleaseLabels(ctx context.Context, req *connect.Request[managev1.SetReleaseLabelsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	command := SetReleaseLabelsCommand{
		ReleaseID: req.Msg.ReleaseId, Labels: ReleaseLabelInputsFromProto(req.Msg.Labels), OrderIntent: RelationOrderIntentFromProto(req.Msg.OrderIntent),
	}
	if req.Msg.Observed != nil {
		command.Observed = &Snapshot[*ReleaseLabelInput]{Items: ReleaseLabelInputsFromProto(req.Msg.Observed.Labels)}
	}
	return respondSuccess(h.releases.SetReleaseLabels(ctx, command))
}

func (h *ConnectHandler) SetReleaseArtists(ctx context.Context, req *connect.Request[managev1.SetReleaseArtistsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	command := SetReleaseArtistsCommand{
		ReleaseID: req.Msg.ReleaseId, Artists: ReleaseArtistInputsFromProto(req.Msg.Artists), OrderIntent: RelationOrderIntentFromProto(req.Msg.OrderIntent),
	}
	if req.Msg.Observed != nil {
		command.Observed = &Snapshot[*ReleaseArtistInput]{Items: ReleaseArtistInputsFromProto(req.Msg.Observed.Artists)}
	}
	return respondSuccess(h.releases.SetReleaseArtists(ctx, command))
}

func (h *ConnectHandler) SetReleaseCategories(ctx context.Context, req *connect.Request[managev1.SetReleaseCategoriesRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return respondSuccess(h.releases.SetReleaseCategories(ctx, SetReleaseCategoriesCommand{
		ReleaseID: req.Msg.ReleaseId, CategoryIDs: req.Msg.CategoryIds, Observed: IDSnapshotFromProto(req.Msg.Observed),
	}))
}

func (h *ConnectHandler) SetReleaseGenres(ctx context.Context, req *connect.Request[managev1.SetReleaseGenresRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return respondSuccess(h.releases.SetReleaseGenres(ctx, SetReleaseGenresCommand{
		ReleaseID: req.Msg.ReleaseId, GenreIDs: req.Msg.GenreIds, Observed: IDSnapshotFromProto(req.Msg.Observed),
	}))
}

func (h *ConnectHandler) SetReleaseStyles(ctx context.Context, req *connect.Request[managev1.SetReleaseStylesRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return respondSuccess(h.releases.SetReleaseStyles(ctx, SetReleaseStylesCommand{
		ReleaseID: req.Msg.ReleaseId, StyleIDs: req.Msg.StyleIds, Observed: IDSnapshotFromProto(req.Msg.Observed),
	}))
}

func (h *ConnectHandler) SetReleaseFormats(ctx context.Context, req *connect.Request[managev1.SetReleaseFormatsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	command := SetReleaseFormatsCommand{ReleaseID: req.Msg.ReleaseId, Formats: ReleaseFormatInputsFromProto(req.Msg.Formats)}
	if req.Msg.Observed != nil {
		command.Observed = &Snapshot[*ReleaseFormatInput]{Items: ReleaseFormatInputsFromProto(req.Msg.Observed.Formats)}
	}
	return respondSuccess(h.releases.SetReleaseFormats(ctx, command))
}

func (h *ConnectHandler) SetReleaseCredits(ctx context.Context, req *connect.Request[managev1.SetReleaseCreditsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	command := SetReleaseCreditsCommand{
		ReleaseID: req.Msg.ReleaseId, Credits: ReleaseCreditInputsFromProto(req.Msg.Credits), OrderIntent: RelationOrderIntentFromProto(req.Msg.OrderIntent),
	}
	if req.Msg.Observed != nil {
		command.Observed = &Snapshot[*CreditInput]{Items: ReleaseCreditInputsFromProto(req.Msg.Observed.Credits)}
	}
	return respondSuccess(h.releases.SetReleaseCredits(ctx, command))
}

func respondSuccess(err error) (*connect.Response[managev1.SuccessResponse], error) {
	return respond(&managev1.SuccessResponse{Success: true}, err)
}

func (h *TrackConnectHandler) ListTracksByRelease(ctx context.Context, req *connect.Request[managev1.ListTracksByReleaseRequest]) (*connect.Response[managev1.ListTracksByReleaseResponse], error) {
	tracks, err := h.releases.ListTracksByRelease(ctx, ListTracksQuery{ReleaseID: req.Msg.ReleaseId})
	return respond(&managev1.ListTracksByReleaseResponse{Tracks: tracks}, err)
}

func (h *TrackConnectHandler) CreateTrack(ctx context.Context, req *connect.Request[managev1.CreateTrackRequest]) (*connect.Response[managev1.Track], error) {
	return respond(h.releases.CreateTrack(ctx, CreateTrackCommand{
		ReleaseID: req.Msg.ReleaseId, Title: req.Msg.Title, DurationSeconds: req.Msg.DurationSeconds, Lyrics: req.Msg.Lyrics,
	}))
}

func (h *TrackConnectHandler) UpdateTrack(ctx context.Context, req *connect.Request[managev1.UpdateTrackRequest]) (*connect.Response[managev1.Track], error) {
	return respond(h.releases.UpdateTrack(ctx, UpdateTrackCommand{
		ID: req.Msg.Id, TrackNumber: req.Msg.TrackNumber, Title: req.Msg.Title,
		DurationSeconds: req.Msg.DurationSeconds, ProcessingStatus: req.Msg.ProcessingStatus, Lyrics: req.Msg.Lyrics,
		ClearDuration: req.Msg.ClearDuration, ClearAudioOriginal: req.Msg.ClearAudioOriginal, ClearLyrics: req.Msg.ClearLyrics,
	}))
}

func (h *TrackConnectHandler) DeleteTrack(ctx context.Context, req *connect.Request[managev1.DeleteTrackRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.releases.DeleteTrack(ctx, DeleteTrackCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *TrackConnectHandler) SetTrackCredits(ctx context.Context, req *connect.Request[managev1.SetTrackCreditsRequest]) (*connect.Response[managev1.TrackWithCredits], error) {
	command := SetTrackCreditsCommand{TrackID: req.Msg.TrackId, Credits: TrackCreditInputsFromProto(req.Msg.Credits)}
	if req.Msg.Observed != nil {
		command.Observed = &Snapshot[*CreditInput]{Items: TrackCreditInputsFromProto(req.Msg.Observed.Credits)}
	}
	return respond(h.releases.SetTrackCredits(ctx, command))
}

func (h *TrackConnectHandler) ReorderTracks(ctx context.Context, req *connect.Request[managev1.ReorderTracksRequest]) (*connect.Response[managev1.ListTracksByReleaseResponse], error) {
	tracks, err := h.releases.ReorderTracks(ctx, ReorderTracksCommand{TrackIDs: req.Msg.TrackIds})
	return respond(&managev1.ListTracksByReleaseResponse{Tracks: tracks}, err)
}

func (h *InternalConnectHandler) LoadReleaseBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadReleaseBlockDocumentRequest]) (*connect.Response[intrav1.LoadReleaseBlockDocumentResponse], error) {
	query := LoadReleaseBlockDocumentQuery{ReleaseID: req.Msg.ReleaseId, Locale: req.Msg.Locale}
	if req.Msg.Principal != nil {
		query.Principal = &CollaborationPrincipal{SessionID: req.Msg.Principal.SessionId}
	}
	return respond(h.releases.LoadReleaseBlockDocument(ctx, query))
}

func (h *InternalConnectHandler) ApplyReleaseBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyReleaseBlockBatchRequest]) (*connect.Response[intrav1.ApplyReleaseBlockBatchResponse], error) {
	save, err := h.releases.ApplyReleaseBlockBatch(ctx, ApplyReleaseBlockBatchCommand{
		ReleaseID: req.Msg.ReleaseId, Locale: req.Msg.Locale, Batch: req.Msg.Batch,
		AffectedLocaleValues: req.Msg.AffectedLocaleValues, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
	})
	return respond(&intrav1.ApplyReleaseBlockBatchResponse{
		Locale: save.Locale, DocumentRevision: save.DocumentRevision, Changed: save.Changed,
		SourceChanged: save.SourceChanged, ChangedLocales: save.ChangedLocales, TargetRevision: save.TargetRevision,
	}, err)
}

func (h *InternalConnectHandler) UpdateReleaseLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateReleaseLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateReleaseLocaleMetadataResponse], error) {
	command := UpdateReleaseLocaleMetadataCommand{
		ReleaseID: req.Msg.ReleaseId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, Title: req.Msg.Title,
		ContributorMemberIDs: req.Msg.ContributorMemberIds,
	}
	if notes := req.Msg.CreditNotes; notes != nil {
		command.CreditNotes = &CreditNotes{Values: make([]*CreditNote, len(notes.Values))}
		for index, value := range notes.Values {
			if value != nil {
				command.CreditNotes.Values[index] = &CreditNote{CreditID: value.CreditId, Note: value.Note}
			}
		}
	}
	save, err := h.releases.UpdateReleaseLocaleMetadata(ctx, command)
	return respond(&intrav1.UpdateReleaseLocaleMetadataResponse{
		Locale: save.Locale, DocumentRevision: save.DocumentRevision, Changed: save.Changed,
		SourceChanged: save.SourceChanged, ChangedLocales: save.ChangedLocales, TargetRevision: save.TargetRevision,
	}, err)
}

// RelationOrderIntentFromProto is the relation order intent of a request.
func RelationOrderIntentFromProto(intent *managev1.RelationOrderIntent) *RelationOrderIntent {
	if intent == nil {
		return nil
	}
	return &RelationOrderIntent{ItemID: intent.ItemId, PreviousItemID: intent.PreviousItemId, NextItemID: intent.NextItemId}
}

// IDSnapshotFromProto is the observed ID set of a request; nil when it was omitted.
func IDSnapshotFromProto(observed *managev1.StringIdSnapshot) *Snapshot[string] {
	if observed == nil {
		return nil
	}
	return &Snapshot[string]{Items: observed.Ids}
}

// ReleaseArtistInputsFromProto converts request Artist entries; a null entry stays
// nil for the use case to reject.
func ReleaseArtistInputsFromProto(inputs []*managev1.ReleaseArtistInput) []*ReleaseArtistInput {
	return convertInputs(inputs, func(input *managev1.ReleaseArtistInput) *ReleaseArtistInput {
		return &ReleaseArtistInput{ArtistID: input.ArtistId}
	})
}

// ReleaseLabelInputsFromProto converts request Label entries.
func ReleaseLabelInputsFromProto(inputs []*managev1.ReleaseLabelInput) []*ReleaseLabelInput {
	return convertInputs(inputs, func(input *managev1.ReleaseLabelInput) *ReleaseLabelInput {
		return &ReleaseLabelInput{LabelID: input.LabelId, CatalogNumber: input.CatalogNumber}
	})
}

// ReleaseFormatInputsFromProto converts request Format entries.
func ReleaseFormatInputsFromProto(inputs []*managev1.ReleaseFormatInput) []*ReleaseFormatInput {
	return convertInputs(inputs, func(input *managev1.ReleaseFormatInput) *ReleaseFormatInput {
		return &ReleaseFormatInput{FormatID: input.FormatId, Description: input.FormatDescription}
	})
}

// ReleaseCreditInputsFromProto converts request Release credit entries.
func ReleaseCreditInputsFromProto(inputs []*managev1.ReleaseCreditInput) []*CreditInput {
	return convertInputs(inputs, func(input *managev1.ReleaseCreditInput) *CreditInput {
		return &CreditInput{
			ID: input.Id, ArtistID: input.ArtistId, MemberID: input.MemberId,
			CreditedName: input.CreditedName, CreditRole: input.CreditRole, SortOrder: input.SortOrder,
		}
	})
}

// TrackCreditInputsFromProto converts request Track credit entries.
func TrackCreditInputsFromProto(inputs []*managev1.TrackCreditInput) []*CreditInput {
	return convertInputs(inputs, func(input *managev1.TrackCreditInput) *CreditInput {
		return &CreditInput{
			ID: input.Id, ArtistID: input.ArtistId, MemberID: input.MemberId,
			CreditedName: input.CreditedName, CreditRole: input.CreditRole, SortOrder: input.SortOrder,
		}
	})
}

func convertInputs[In any, Out any](inputs []*In, convert func(*In) *Out) []*Out {
	if inputs == nil {
		return nil
	}
	converted := make([]*Out, len(inputs))
	for index, input := range inputs {
		if input != nil {
			converted[index] = convert(input)
		}
	}
	return converted
}

func optionalTime(value *timestamppb.Timestamp) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.AsTime()
	return &converted
}
