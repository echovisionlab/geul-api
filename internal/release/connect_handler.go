package release

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ConnectHandler serves the administrator Release RPCs. Each method
// translates the request into a use-case command or query, and the result
// into the response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	releases *Service
}

// NewConnectHandler returns the administrator Release Connect adapter.
func NewConnectHandler(releases *Service) *ConnectHandler {
	return &ConnectHandler{releases: releases}
}

// TrackConnectHandler serves the administrator Track RPCs.
type TrackConnectHandler struct {
	releases *Service
}

// NewTrackConnectHandler returns the administrator Track Connect adapter.
func NewTrackConnectHandler(releases *Service) *TrackConnectHandler {
	return &TrackConnectHandler{releases: releases}
}

// InternalConnectHandler serves the collaboration service's typed Release
// Block boundary.
type InternalConnectHandler struct {
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
	view, err := h.releases.GetRelease(ctx, GetReleaseQuery{ID: req.Msg.Id})
	return respond(ReleaseMessage(view), err)
}

func (h *ConnectHandler) GetReleaseRelations(ctx context.Context, req *connect.Request[managev1.GetReleaseRelationsRequest]) (*connect.Response[managev1.GetReleaseRelationsResponse], error) {
	relations, err := h.releases.GetReleaseRelations(ctx, GetReleaseRelationsQuery{ReleaseID: req.Msg.ReleaseId})
	return respond(RelationsMessage(relations), err)
}

func (h *ConnectHandler) ListReleasesAdmin(ctx context.Context, req *connect.Request[managev1.ListReleasesAdminRequest]) (*connect.Response[managev1.ListReleasesAdminResponse], error) {
	page, err := h.releases.ListReleasesAdmin(ctx, ListReleasesQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
		Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&managev1.ListReleasesAdminResponse{
		Releases: ReleasesWithStatsMessage(page.Releases),
		Pagination: &commonv1.PaginationResponse{
			Total: int32(page.Total), Limit: page.Limit, Offset: page.Offset,
			HasMore: page.Offset+page.Limit < int32(page.Total),
		},
	}, err)
}

func (h *ConnectHandler) CreateRelease(ctx context.Context, req *connect.Request[managev1.CreateReleaseRequest]) (*connect.Response[managev1.Release], error) {
	view, err := h.releases.CreateRelease(ctx, CreateReleaseCommand{
		Title: req.Msg.Title, SourceLocale: req.Msg.SourceLocale, AcceptLanguage: req.Header().Get("Accept-Language"),
		Type: releaseTypeFromProto(req.Msg.Type), Slug: req.Msg.Slug, ReleaseDate: optionalTime(req.Msg.ReleaseDate),
		SpotifyURL: req.Msg.SpotifyUrl, AppleMusicURL: req.Msg.AppleMusicUrl, BandcampURL: req.Msg.BandcampUrl,
		YoutubeMusicURL: req.Msg.YoutubeMusicUrl, CatalogNumber: req.Msg.CatalogNumber, Document: req.Msg.Document,
	})
	return respond(ReleaseMessage(view), err)
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
	return respond(&managev1.SetReleaseArtworkResponse{ArtworkAsset: mediaasset.ReferenceProto(asset)}, err)
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
	return respond(&managev1.ListTracksByReleaseResponse{Tracks: TracksWithCreditsMessage(ctx, tracks)}, err)
}

func (h *TrackConnectHandler) CreateTrack(ctx context.Context, req *connect.Request[managev1.CreateTrackRequest]) (*connect.Response[managev1.Track], error) {
	track, err := h.releases.CreateTrack(ctx, CreateTrackCommand{
		ReleaseID: req.Msg.ReleaseId, Title: req.Msg.Title, DurationSeconds: req.Msg.DurationSeconds, Lyrics: req.Msg.Lyrics,
	})
	return respond(TrackMessage(ctx, track), err)
}

func (h *TrackConnectHandler) UpdateTrack(ctx context.Context, req *connect.Request[managev1.UpdateTrackRequest]) (*connect.Response[managev1.Track], error) {
	track, err := h.releases.UpdateTrack(ctx, UpdateTrackCommand{
		ID: req.Msg.Id, TrackNumber: req.Msg.TrackNumber, Title: req.Msg.Title,
		DurationSeconds: req.Msg.DurationSeconds, ProcessingStatus: req.Msg.ProcessingStatus, Lyrics: req.Msg.Lyrics,
		ClearDuration: req.Msg.ClearDuration, ClearAudioOriginal: req.Msg.ClearAudioOriginal, ClearLyrics: req.Msg.ClearLyrics,
	})
	return respond(TrackMessage(ctx, track), err)
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
	track, err := h.releases.SetTrackCredits(ctx, command)
	return respond(TrackWithCreditsMessage(ctx, track), err)
}

func (h *TrackConnectHandler) ReorderTracks(ctx context.Context, req *connect.Request[managev1.ReorderTracksRequest]) (*connect.Response[managev1.ListTracksByReleaseResponse], error) {
	tracks, err := h.releases.ReorderTracks(ctx, ReorderTracksCommand{TrackIDs: req.Msg.TrackIds})
	return respond(&managev1.ListTracksByReleaseResponse{Tracks: TracksWithCreditsMessage(ctx, tracks)}, err)
}

func (h *InternalConnectHandler) LoadReleaseBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadReleaseBlockDocumentRequest]) (*connect.Response[intrav1.LoadReleaseBlockDocumentResponse], error) {
	query := LoadReleaseBlockDocumentQuery{ReleaseID: req.Msg.ReleaseId, Locale: req.Msg.Locale}
	if req.Msg.Principal != nil {
		query.Principal = &CollaborationPrincipal{SessionID: req.Msg.Principal.SessionId}
	}
	document, err := h.releases.LoadReleaseBlockDocument(ctx, query)
	return respond(blockDocumentMessage(document), err)
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

func timestampProtoPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

// ReleaseMessage renders a Release view; its artwork is its Open Graph image
// as well.
func ReleaseMessage(view View) *managev1.Release {
	r := view.Release
	artwork := mediaasset.ReferenceProto(view.Artwork)
	return &managev1.Release{
		Id:              r.ID,
		Title:           view.Title,
		Type:            releaseTypeToProto(r.Type),
		Document:        view.Document,
		SourceLocale:    r.SourceLocale,
		Status:          string(r.Status),
		CreatedAt:       timestamppb.New(r.CreatedAt),
		UpdatedAt:       timestamppb.New(r.UpdatedAt),
		OgAsset:         artwork,
		ArtworkAsset:    artwork,
		Revision:        view.Revision,
		Slug:            r.Slug,
		ReleaseDate:     timestampProtoPtr(r.ReleaseDate),
		PublishedAt:     timestampProtoPtr(r.PublishedAt),
		SpotifyUrl:      r.SpotifyURL,
		AppleMusicUrl:   r.AppleMusicURL,
		BandcampUrl:     r.BandcampURL,
		YoutubeMusicUrl: r.YoutubeMusicURL,
		CatalogNumber:   r.CatalogNumber,
	}
}

// ReleasesWithStatsMessage renders a page of Releases with their counts.
func ReleasesWithStatsMessage(releases []ReleaseWithStats) []*managev1.ReleaseWithStats {
	result := make([]*managev1.ReleaseWithStats, len(releases))
	for i, release := range releases {
		result[i] = &managev1.ReleaseWithStats{
			Release: ReleaseMessage(release.View), TrackCount: release.TrackCount, CreditCount: release.CreditCount,
		}
	}
	return result
}

// RelationsMessage renders a Release's editable relation snapshot.
func RelationsMessage(relations Relations) *managev1.GetReleaseRelationsResponse {
	response := &managev1.GetReleaseRelationsResponse{
		ReleaseId:  relations.ReleaseID,
		Artists:    make([]*managev1.ReleaseArtistEditorItem, 0, len(relations.Artists)),
		Labels:     make([]*managev1.ReleaseLabelEditorItem, 0, len(relations.Labels)),
		Categories: referenceItems(relations.Categories),
		Genres:     referenceItems(relations.Genres),
		Styles:     referenceItems(relations.Styles),
		Formats:    make([]*managev1.ReleaseFormatEditorItem, 0, len(relations.Formats)),
		Credits:    make([]*managev1.ReleaseCreditEditorItem, 0, len(relations.Credits)),
	}
	for _, artist := range relations.Artists {
		response.Artists = append(response.Artists, &managev1.ReleaseArtistEditorItem{
			ArtistId: artist.ArtistID, ArtistName: artist.Name, ArtistSlug: artist.Slug, SortOrder: artist.SortOrder,
		})
	}
	for _, label := range relations.Labels {
		response.Labels = append(response.Labels, &managev1.ReleaseLabelEditorItem{
			LabelId: label.LabelID, LabelName: label.Name, LabelSlug: label.Slug,
			CatalogNumber: label.CatalogNumber, SortOrder: label.SortOrder,
		})
	}
	for _, format := range relations.Formats {
		response.Formats = append(response.Formats, &managev1.ReleaseFormatEditorItem{
			Id: format.ID, Name: format.Name, Slug: format.Slug, FormatDescription: format.Description,
		})
	}
	for _, credit := range relations.Credits {
		response.Credits = append(response.Credits, &managev1.ReleaseCreditEditorItem{
			Id: credit.ID, ArtistId: credit.ArtistID, ArtistName: credit.ArtistName, ArtistSlug: credit.ArtistSlug,
			MemberId: credit.MemberID, MemberName: credit.MemberName,
			CreditedName: credit.CreditedName, CreditRole: credit.CreditRole, SortOrder: credit.SortOrder,
		})
	}
	return response
}

func referenceItems(references []Reference) []*managev1.ReleaseReferenceEditorItem {
	items := make([]*managev1.ReleaseReferenceEditorItem, 0, len(references))
	for _, reference := range references {
		items = append(items, &managev1.ReleaseReferenceEditorItem{Id: reference.ID, Name: reference.Name, Slug: reference.Slug})
	}
	return items
}

// TrackMessage renders a Track. Metadata that cannot be rendered is omitted
// and logged.
func TrackMessage(ctx context.Context, t Track) *managev1.Track {
	message := &managev1.Track{
		Id:                  t.ID,
		ReleaseId:           t.ReleaseID,
		TrackNumber:         int32(t.TrackNumber),
		Title:               t.Title,
		CreatedAt:           timestamppb.New(t.CreatedAt),
		AudioOriginalFileId: t.AudioOriginalFileID,
		ProcessingStatus:    t.ProcessingStatus,
		Lyrics:              t.Lyrics,
	}
	if t.DurationSeconds != nil {
		duration := int32(*t.DurationSeconds)
		message.DurationSeconds = &duration
	}
	if t.Metadata != nil {
		if metadata, err := structpb.NewStruct(t.Metadata); err == nil {
			message.Metadata = metadata
		} else {
			slog.ErrorContext(ctx, "Track metadata could not be converted; omitting it from the response", "domain", "release", "operation", "convert_track_metadata", failurecause.Attr(err), "track_id", t.ID)
		}
	}
	return message
}

// TrackWithCreditsMessage renders a Track with its credits.
func TrackWithCreditsMessage(ctx context.Context, track TrackWithCredits) *managev1.TrackWithCredits {
	credits := make([]*managev1.TrackCredit, len(track.Credits))
	for i, credit := range track.Credits {
		credits[i] = &managev1.TrackCredit{
			Id:           credit.ID,
			CreditType:   credit.CreditType,
			SortOrder:    int32(credit.SortOrder),
			ArtistId:     credit.ArtistID,
			ArtistName:   credit.ArtistName,
			ArtistSlug:   credit.ArtistSlug,
			MemberId:     credit.MemberID,
			CreditedName: credit.CreditedName,
			CreditRole:   credit.CreditRole,
			Member:       memberref.ReferenceMessage(credit.Member),
		}
	}
	return &managev1.TrackWithCredits{Track: TrackMessage(ctx, track.Track), Credits: credits}
}

// TracksWithCreditsMessage renders Tracks with their credits in order.
func TracksWithCreditsMessage(ctx context.Context, tracks []TrackWithCredits) []*managev1.TrackWithCredits {
	if tracks == nil {
		return nil
	}
	result := make([]*managev1.TrackWithCredits, len(tracks))
	for i, track := range tracks {
		result[i] = TrackWithCreditsMessage(ctx, track)
	}
	return result
}

func blockDocumentMessage(document BlockDocument) *intrav1.LoadReleaseBlockDocumentResponse {
	artists := make([]*intrav1.ReleaseArtistState, 0, len(document.Artists))
	for _, artist := range document.Artists {
		artists = append(artists, &intrav1.ReleaseArtistState{
			ArtistId: artist.ArtistID, ArtistName: artist.Name, ArtistSlug: artist.Slug, SortOrder: artist.SortOrder,
		})
	}
	response := &intrav1.LoadReleaseBlockDocumentResponse{
		Document: document.Document, DocumentRevision: document.DocumentRevision,
		SourceMetadata: releaseLocaleMetadataMessage(document.SourceMetadata), Artists: artists,
		Locale: document.Locale, LocaleExists: document.LocaleExists,
		PresentLocaleValues: document.PresentLocaleValues, TargetRevision: document.TargetRevision,
	}
	if document.LocaleMetadata != nil {
		response.LocaleMetadata = releaseLocaleMetadataMessage(*document.LocaleMetadata)
	}
	return response
}

func releaseLocaleMetadataMessage(locale LocaleNotes) *intrav1.ReleaseLocaleMetadata {
	creditIDs := make([]string, 0, len(locale.Notes))
	for creditID := range locale.Notes {
		creditIDs = append(creditIDs, creditID)
	}
	sort.Strings(creditIDs)
	message := &intrav1.ReleaseLocaleMetadata{Locale: locale.Metadata.Locale, Title: locale.Metadata.Title}
	for _, creditID := range creditIDs {
		message.CreditNotes = append(message.CreditNotes, &intrav1.ReleaseCreditNote{CreditId: creditID, Note: locale.Notes[creditID]})
	}
	return message
}

// releaseStatusToProto maps a stored Release status to the API enum. A value
// the API does not know is UNSPECIFIED.
func releaseStatusToProto(status Status) managev1.ReleaseStatus {
	switch status {
	case StatusDraft:
		return managev1.ReleaseStatus_RELEASE_STATUS_DRAFT
	case StatusPublished:
		return managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED
	default:
		return managev1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED
	}
}

// releaseStatusFromProto maps an API Release status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func releaseStatusFromProto(status managev1.ReleaseStatus) (Status, bool) {
	switch status {
	case managev1.ReleaseStatus_RELEASE_STATUS_DRAFT:
		return StatusDraft, true
	case managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED:
		return StatusPublished, true
	default:
		return "", false
	}
}

// releaseTypeToProto maps a stored Release type to the API enum. A value the
// API does not know reads as an album, the column default.
func releaseTypeToProto(releaseType Type) managev1.ReleaseType {
	switch releaseType {
	case TypeEP:
		return managev1.ReleaseType_RELEASE_TYPE_EP
	case TypeSingle:
		return managev1.ReleaseType_RELEASE_TYPE_SINGLE
	case TypeCompilation:
		return managev1.ReleaseType_RELEASE_TYPE_COMPILATION
	default:
		return managev1.ReleaseType_RELEASE_TYPE_ALBUM
	}
}

// releaseTypeFromProto maps an API Release type to the stored value.
// UNSPECIFIED and unknown values store an album, the column default.
func releaseTypeFromProto(releaseType managev1.ReleaseType) Type {
	switch releaseType {
	case managev1.ReleaseType_RELEASE_TYPE_EP:
		return TypeEP
	case managev1.ReleaseType_RELEASE_TYPE_SINGLE:
		return TypeSingle
	case managev1.ReleaseType_RELEASE_TYPE_COMPILATION:
		return TypeCompilation
	default:
		return TypeAlbum
	}
}
