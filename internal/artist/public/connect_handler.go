package public

import (
	"context"

	"connectrpc.com/connect"
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the public Artist RPCs by translating each request
// into a query of the public Artist reads.
type ConnectHandler struct {
	openv1connect.UnimplementedArtistServiceHandler
	artists *Service
}

// NewConnectHandler returns the public Connect adapter of artists.
func NewConnectHandler(artists *Service) *ConnectHandler {
	return &ConnectHandler{artists: artists}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListArtistsRequest]) (*connect.Response[openv1.ListArtistsResponse], error) {
	page, err := h.artists.List(ctx, ListArtistsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	artists := make([]*openv1.ArtistSummary, 0, len(page.Artists))
	for _, summary := range page.Artists {
		artists = append(artists, summaryMessage(summary))
	}
	return respond(&openv1.ListArtistsResponse{Artists: artists, Pagination: &commonv1.PaginationResponse{
		Total: page.Pagination.Total, Limit: page.Pagination.Limit, Offset: page.Pagination.Offset, HasMore: page.Pagination.HasMore,
	}}, nil)
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetArtistRequest]) (*connect.Response[openv1.GetArtistResponse], error) {
	detail, err := h.artists.Get(ctx, GetArtistQuery{
		SlugOrID: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&openv1.GetArtistResponse{Artist: detailMessage(detail)}, nil)
}

func (h *ConnectHandler) GetReleases(ctx context.Context, req *connect.Request[openv1.GetArtistReleasesRequest]) (*connect.Response[openv1.GetArtistReleasesResponse], error) {
	page, err := h.artists.GetReleases(ctx, ArtistWorksQuery{
		ArtistID: req.Msg.ArtistId, Limit: req.Msg.Limit, Offset: req.Msg.Offset, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	releases := make([]*openv1.ArtistRelease, 0, len(page.Releases))
	for _, release := range page.Releases {
		item := &openv1.ArtistRelease{Id: release.ID, Title: release.Title, Slug: release.Slug, Type: release.Type, ArtworkAsset: release.Artwork}
		if release.ReleaseDate != nil {
			item.ReleaseDate = timestamppb.New(*release.ReleaseDate)
		}
		if release.PublishedAt != nil {
			item.PublishedAt = timestamppb.New(*release.PublishedAt)
		}
		for _, artist := range release.Artists {
			item.Artists = append(item.Artists, &openv1.ArtistReleaseArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug})
		}
		releases = append(releases, item)
	}
	return respond(&openv1.GetArtistReleasesResponse{Releases: releases, Total: page.Total}, nil)
}

func (h *ConnectHandler) GetWorks(ctx context.Context, req *connect.Request[openv1.GetArtistWorksRequest]) (*connect.Response[openv1.GetArtistWorksResponse], error) {
	page, err := h.artists.GetWorks(ctx, ArtistWorksQuery{
		ArtistID: req.Msg.ArtistId, Limit: req.Msg.Limit, Offset: req.Msg.Offset, Sorts: req.Msg.Sorts,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	works := make([]*openv1.ArtistWork, 0, len(page.Works))
	for _, work := range page.Works {
		item := &openv1.ArtistWork{
			Id: work.ID, Title: work.Title, Slug: work.Slug, Summary: work.Summary,
			Type: artistWorkTypeFromDatabase(work.Type), ImageAsset: work.Image,
		}
		if work.PublishedAt != nil {
			item.PublishedAt = timestamppb.New(*work.PublishedAt)
		}
		works = append(works, item)
	}
	return respond(&openv1.GetArtistWorksResponse{Works: works, Total: page.Total}, nil)
}

func summaryMessage(summary Summary) *openv1.ArtistSummary {
	message := &openv1.ArtistSummary{
		Id: summary.ID, Name: summary.Name, Slug: summary.Slug, CountryCode: summary.CountryCode,
		SocialLinks: summary.SocialLinks, ImageAsset: summary.Image, OgAsset: summary.OgImage,
	}
	if summary.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*summary.PublishedAt)
	}
	return message
}

func detailMessage(detail Detail) *openv1.Artist {
	value := detail.Artist
	message := &openv1.Artist{
		Id:          value.ID,
		Name:        detail.Name,
		Document:    detail.Document,
		SocialLinks: value.SocialLinks,
		Status:      publicArtistStatus(value.Status),
		CreatedAt:   timestamppb.New(value.CreatedAt),
		IsGroup:     detail.IsGroup,
		Revision:    detail.Revision,
		Slug:        value.Slug,
		RealName:    value.RealName,
		CountryCode: value.CountryCode,
		Website:     value.Website,
		OgAsset:     detail.OgImage,
	}
	if value.UpdatedAt != nil {
		message.UpdatedAt = timestamppb.New(*value.UpdatedAt)
	}
	message.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(detail.Localization)
	if value.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*value.PublishedAt)
	}
	message.Images = make([]*openv1.ArtistImage, 0, len(detail.Images))
	for index, image := range detail.Images {
		primary := index == 0
		message.Images = append(message.Images, &openv1.ArtistImage{FileId: image.FileID, Asset: image.Asset, SortOrder: int32(index), Primary: primary})
		if primary {
			message.ImageAsset = image.Asset
		}
	}
	message.ReleaseCount, message.WorkCount = detail.ReleaseCount, detail.WorkCount
	message.Labels = make([]*openv1.ArtistLabel, 0, len(detail.Labels))
	for _, label := range detail.Labels {
		message.Labels = append(message.Labels, &openv1.ArtistLabel{Id: label.ID, Name: label.Name, Slug: label.Slug, ImageAsset: label.Image})
	}
	return message
}

// publicArtistStatus maps a stored Artist status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func publicArtistStatus(status artistdomain.Status) openv1.ArtistStatus {
	switch status {
	case artistdomain.StatusDraft:
		return openv1.ArtistStatus_ARTIST_STATUS_DRAFT
	case artistdomain.StatusPublished:
		return openv1.ArtistStatus_ARTIST_STATUS_PUBLISHED
	default:
		return openv1.ArtistStatus_ARTIST_STATUS_UNSPECIFIED
	}
}

func artistWorkTypeFromDatabase(value string) openv1.WorkType {
	if enumValue, ok := openv1.WorkType_value[value]; ok {
		return openv1.WorkType(enumValue)
	}
	return openv1.WorkType_WORK_TYPE_UNSPECIFIED
}
