package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the public Label RPCs by translating each request
// into a query of the public Label reads.
type ConnectHandler struct {
	labels *Service
}

// NewConnectHandler returns the public Connect adapter of labels.
func NewConnectHandler(labels *Service) *ConnectHandler {
	return &ConnectHandler{labels: labels}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListLabelsRequest]) (*connect.Response[openv1.ListLabelsResponse], error) {
	page, err := h.labels.List(ctx, ListLabelsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	labels := make([]*openv1.LabelSummary, 0, len(page.Labels))
	for _, summary := range page.Labels {
		labels = append(labels, summaryMessage(summary))
	}
	return respond(&openv1.ListLabelsResponse{Labels: labels, Pagination: &commonv1.PaginationResponse{
		Total: page.Pagination.Total, Limit: page.Pagination.Limit, Offset: page.Pagination.Offset, HasMore: page.Pagination.HasMore,
	}}, nil)
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetLabelRequest]) (*connect.Response[openv1.GetLabelResponse], error) {
	detail, err := h.labels.Get(ctx, GetLabelQuery{
		SlugOrID: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&openv1.GetLabelResponse{Label: detailMessage(detail)}, nil)
}

func summaryMessage(summary Summary) *openv1.LabelSummary {
	message := &openv1.LabelSummary{
		Id: summary.ID, Name: summary.Name, Slug: summary.Slug, CountryCode: summary.CountryCode, Website: summary.Website,
		OgAsset: mediaasset.ReferenceProto(summary.OgImage), ImageLightAsset: mediaasset.ReferenceProto(summary.LightLogo), ImageDarkAsset: mediaasset.ReferenceProto(summary.DarkLogo),
	}
	if summary.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*summary.PublishedAt)
	}
	return message
}

func detailMessage(detail Detail) *openv1.Label {
	value := detail.Label
	message := &openv1.Label{
		Id:          value.ID,
		Name:        detail.Name,
		Document:    detail.Document,
		SocialLinks: value.SocialLinks,
		Status:      publicLabelStatus(value.Status),
		CreatedAt:   timestamppb.New(value.CreatedAt),
		UpdatedAt:   timestamppb.New(value.UpdatedAt),
		Revision:    detail.Revision,
		Slug:        value.Slug,
		CountryCode: value.CountryCode,
		Website:     value.Website,
		OgAsset:     mediaasset.ReferenceProto(detail.OgImage),
	}
	if value.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*value.PublishedAt)
	}
	message.ImageLightAsset, message.ImageDarkAsset = mediaasset.ReferenceProto(detail.LightLogo), mediaasset.ReferenceProto(detail.DarkLogo)
	message.ReleaseCount, message.ArtistCount = detail.ReleaseCount, detail.ArtistCount
	if detail.Parent != nil {
		message.ParentLabel = &openv1.LabelSummary{
			Id: detail.Parent.ID, Name: detail.Parent.Name, Slug: detail.Parent.Slug,
			ImageLightAsset: mediaasset.ReferenceProto(detail.Parent.LightLogo), ImageDarkAsset: mediaasset.ReferenceProto(detail.Parent.DarkLogo), OgAsset: mediaasset.ReferenceProto(detail.Parent.OgImage),
		}
	}
	message.Artists = make([]*openv1.LabelArtist, 0, len(detail.Artists))
	for _, artist := range detail.Artists {
		message.Artists = append(message.Artists, &openv1.LabelArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug, ImageAsset: mediaasset.ReferenceProto(artist.Image)})
	}
	message.Releases = make([]*openv1.LabelRelease, 0, len(detail.Releases))
	for _, release := range detail.Releases {
		item := &openv1.LabelRelease{Id: release.ID, Title: release.Title, Slug: release.Slug, Type: release.Type, ArtworkAsset: mediaasset.ReferenceProto(release.Artwork)}
		if release.ReleaseDate != nil {
			item.ReleaseDate = timestamppb.New(*release.ReleaseDate)
		}
		if release.PublishedAt != nil {
			item.PublishedAt = timestamppb.New(*release.PublishedAt)
		}
		for _, artist := range release.Artists {
			item.Artists = append(item.Artists, &openv1.LabelReleaseArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug})
		}
		message.Releases = append(message.Releases, item)
	}
	message.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(detail.Localization)
	return message
}

// publicLabelStatus maps a stored Label status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func publicLabelStatus(status label.Status) openv1.LabelStatus {
	switch status {
	case label.StatusDraft:
		return openv1.LabelStatus_LABEL_STATUS_DRAFT
	case label.StatusPublished:
		return openv1.LabelStatus_LABEL_STATUS_PUBLISHED
	default:
		return openv1.LabelStatus_LABEL_STATUS_UNSPECIFIED
	}
}
