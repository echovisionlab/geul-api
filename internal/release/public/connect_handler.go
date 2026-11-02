package public

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/release"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Release RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedReleaseServiceHandler
	releases *Service
}

// NewConnectHandler returns the public Release Connect adapter.
func NewConnectHandler(releases *Service) *ConnectHandler {
	return &ConnectHandler{releases: releases}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetReleaseRequest]) (*connect.Response[openv1.GetReleaseResponse], error) {
	view, err := h.releases.GetRelease(ctx, GetReleaseQuery{
		SlugOrID: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respond(&openv1.GetReleaseResponse{Release: releaseMessage(view)}, err)
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListReleasesRequest]) (*connect.Response[openv1.ListReleasesResponse], error) {
	page, err := h.releases.ListReleases(ctx, ListReleasesQuery{
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
		Limit: req.Msg.Pagination.GetLimit(), Offset: req.Msg.Pagination.GetOffset(),
	})
	return respond(&openv1.ListReleasesResponse{
		Releases: summaryMessages(page.Releases),
		Pagination: &commonv1.PaginationResponse{
			Total: int32(page.Total), Limit: page.Limit, Offset: page.Offset,
			HasMore: page.Offset+int32(len(page.Releases)) < int32(page.Total),
		},
	}, err)
}

func timestampPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func summaryMessages(summaries []Summary) []*openv1.ReleaseSummary {
	if summaries == nil {
		return nil
	}
	messages := make([]*openv1.ReleaseSummary, 0, len(summaries))
	for _, summary := range summaries {
		messages = append(messages, releaseSummaryMessage(summary))
	}
	return messages
}

// releaseSummaryMessage renders a release summary (no tracks); its artwork
// is its Open Graph image as well.
func releaseSummaryMessage(summary Summary) *openv1.ReleaseSummary {
	r := summary.Release
	artwork := mediaasset.ReferenceProto(summary.Artwork)
	return &openv1.ReleaseSummary{
		Id:           r.ID,
		Title:        summary.Title,
		Type:         releaseTypeToOpen(r.Type),
		Slug:         r.Slug,
		ReleaseDate:  timestampPtr(r.ReleaseDate),
		PublishedAt:  timestampPtr(r.PublishedAt),
		OgAsset:      artwork,
		ArtworkAsset: artwork,
		Artists:      artistMessages(summary.Artists),
	}
}

func releaseMessage(view View) *openv1.Release {
	r := view.Release
	artwork := mediaasset.ReferenceProto(view.Artwork)
	message := &openv1.Release{
		Id:               r.ID,
		Title:            view.Title,
		Type:             releaseTypeToOpen(r.Type),
		Document:         view.Document,
		Status:           releaseStatusToOpen(r.Status),
		CreatedAt:        timestamppb.New(r.CreatedAt),
		UpdatedAt:        timestamppb.New(r.UpdatedAt),
		Revision:         view.Revision,
		Slug:             r.Slug,
		LocalizationInfo: publiccontent.ToProtoLocalizationInfo(view.Localization),
		CatalogNumber:    r.CatalogNumber,
		ReleaseDate:      timestampPtr(r.ReleaseDate),
		PublishedAt:      timestampPtr(r.PublishedAt),
		SpotifyUrl:       r.SpotifyURL,
		AppleMusicUrl:    r.AppleMusicURL,
		BandcampUrl:      r.BandcampURL,
		YoutubeMusicUrl:  r.YoutubeMusicURL,
		ArtworkAsset:     artwork,
		OgAsset:          artwork,
		Artists:          artistMessages(view.Artists),
		Labels:           make([]*openv1.ReleaseLabel, 0, len(view.Labels)),
		Genres:           referenceMessages(view.Genres),
		Styles:           referenceMessages(view.Styles),
		Formats:          make([]*openv1.ReleaseFormat, 0, len(view.Formats)),
		Credits:          make([]*openv1.ReleaseCredit, 0, len(view.Credits)),
		Tracks:           make([]*openv1.ReleaseTrack, 0, len(view.Tracks)),
	}
	for _, label := range view.Labels {
		message.Labels = append(message.Labels, &openv1.ReleaseLabel{
			Id: label.ID, Name: label.Name, Slug: label.Slug, CatalogNumber: label.CatalogNumber,
		})
	}
	for _, format := range view.Formats {
		projected := &openv1.ReleaseFormat{Id: format.ID, Name: format.Name, Description: format.Description}
		if format.Slug != "" {
			projected.Slug = &format.Slug
		}
		message.Formats = append(message.Formats, projected)
	}
	for _, credit := range view.Credits {
		message.Credits = append(message.Credits, &openv1.ReleaseCredit{
			Id: credit.ID, Name: credit.Name, Slug: credit.Slug, CreditRole: credit.CreditRole,
			ArtistId: credit.ArtistID, MemberId: credit.MemberID, Member: memberref.ReferenceMessage(credit.Member), Note: credit.Note,
			ImageAsset: mediaasset.ReferenceProto(credit.Image),
		})
	}
	for _, track := range view.Tracks {
		message.Tracks = append(message.Tracks, trackMessage(track))
	}
	return message
}

func artistMessages(artists []Artist) []*openv1.ReleaseArtist {
	if artists == nil {
		return nil
	}
	messages := make([]*openv1.ReleaseArtist, 0, len(artists))
	for _, artist := range artists {
		messages = append(messages, artistMessage(artist))
	}
	return messages
}

func artistMessage(artist Artist) *openv1.ReleaseArtist {
	return &openv1.ReleaseArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug, Role: artist.Role, ImageAsset: mediaasset.ReferenceProto(artist.Image)}
}

func referenceMessages(references []release.Reference) []*openv1.ReleaseReference {
	messages := make([]*openv1.ReleaseReference, 0, len(references))
	for _, reference := range references {
		projected := &openv1.ReleaseReference{Id: reference.ID, Name: reference.Name}
		if reference.Slug != "" {
			projected.Slug = &reference.Slug
		}
		messages = append(messages, projected)
	}
	return messages
}

func trackMessage(track Track) *openv1.ReleaseTrack {
	message := &openv1.ReleaseTrack{
		Id:                   track.ID,
		Title:                track.Title,
		TrackNumber:          int32(track.TrackNumber),
		DownloadAvailability: downloadAvailabilityToOpen(track.Download.Availability),
		DownloadAction:       downloadActionToOpen(track.Download.Action),
		Delivery:             trackMediaDelivery(track.DurationSeconds, track.Media),
	}
	if track.Credits != nil {
		message.Credits = make([]*openv1.TrackCredit, 0, len(track.Credits))
	}
	for _, credit := range track.Credits {
		projected := &openv1.TrackCredit{Id: credit.ID, Name: credit.Name, CreditRole: credit.CreditRole}
		if credit.Artist != nil {
			projected.Artist = artistMessage(*credit.Artist)
		}
		message.Credits = append(message.Credits, projected)
	}
	if track.DurationSeconds != nil {
		durationMs := int32(*track.DurationSeconds * 1000)
		message.DurationMs = &durationMs
	}
	return message
}

// trackMediaDelivery is the delivery of a Track's original audio once its
// playback, waveform and spectrogram are ready.
func trackMediaDelivery(durationSeconds *int, media TrackMedia) *commonv1.MediaDelivery {
	if media.Source.ID == "" || media.Playback == nil || media.Waveform == nil || media.Spectrogram == nil {
		return nil
	}
	delivery := &commonv1.MediaDelivery{
		FileId:           media.Source.ID,
		Extension:        media.Source.Extension,
		MimeType:         media.Source.MimeType,
		FileSize:         media.Source.FileSize,
		FileName:         media.Source.FileName,
		Playback:         media.Playback,
		Download:         media.Download,
		Waveform:         mediaasset.ReferenceProto(media.Waveform),
		Spectrogram:      mediaasset.ReferenceProto(media.Spectrogram),
		ProcessingStatus: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
	}
	if durationSeconds != nil {
		duration := int32(*durationSeconds)
		delivery.DurationSeconds = &duration
	}
	return delivery
}

func downloadAvailabilityToOpen(availability DownloadAvailability) openv1.FileDownloadAvailability {
	if availability == DownloadAvailable {
		return openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE
	}
	return openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE
}

func downloadActionToOpen(action DownloadAction) openv1.FileDownloadAction {
	switch action {
	case DownloadActionDownload:
		return openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD
	case DownloadActionSignIn:
		return openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_SIGN_IN
	default:
		return openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE
	}
}

// releaseStatusToOpen maps a stored Release status to the public API enum.
// A value the API does not know is UNSPECIFIED.
func releaseStatusToOpen(status release.Status) openv1.ReleaseStatus {
	switch status {
	case release.StatusDraft:
		return openv1.ReleaseStatus_RELEASE_STATUS_DRAFT
	case release.StatusPublished:
		return openv1.ReleaseStatus_RELEASE_STATUS_PUBLISHED
	default:
		return openv1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED
	}
}

// releaseTypeToOpen maps a stored Release type to the public API enum. A
// value the API does not know is UNSPECIFIED.
func releaseTypeToOpen(releaseType release.Type) openv1.ReleaseType {
	switch releaseType {
	case release.TypeAlbum:
		return openv1.ReleaseType_RELEASE_TYPE_ALBUM
	case release.TypeEP:
		return openv1.ReleaseType_RELEASE_TYPE_EP
	case release.TypeSingle:
		return openv1.ReleaseType_RELEASE_TYPE_SINGLE
	case release.TypeCompilation:
		return openv1.ReleaseType_RELEASE_TYPE_COMPILATION
	default:
		return openv1.ReleaseType_RELEASE_TYPE_UNSPECIFIED
	}
}
