package public

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/memberref"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/publiccontent"

	workdomain "github.com/echovisionlab/geul-api/internal/work"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the public Work RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedWorkServiceHandler
	works *Service
}

// NewConnectHandler returns the Connect adapter of works.
func NewConnectHandler(works *Service) *ConnectHandler {
	return &ConnectHandler{works: works}
}

func render[R, T any](result R, err error, message func(R) *T) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message(result)), nil
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetWorkRequest]) (*connect.Response[openv1.GetWorkResponse], error) {
	detail, err := h.works.Get(ctx, GetWorkQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(detail, err, func(detail Detail) *openv1.GetWorkResponse {
		return &openv1.GetWorkResponse{Work: workMessage(ctx, detail), BlockMedia: fileref.BlockMediaItemsProto(detail.BlockMedia)}
	})
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListWorksRequest]) (*connect.Response[openv1.ListWorksResponse], error) {
	listed, err := h.works.List(ctx, ListWorksQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(listed, err, func(listed WorkPage) *openv1.ListWorksResponse {
		summaries := make([]*openv1.WorkSummary, 0, len(listed.Works))
		for _, summary := range listed.Works {
			summaries = append(summaries, summaryMessage(summary))
		}
		pagination := listed.Pagination
		return &openv1.ListWorksResponse{Works: summaries, Pagination: &commonv1.PaginationResponse{
			Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
		}}
	})
}

func (h *ConnectHandler) ListMapFeatures(ctx context.Context, req *connect.Request[openv1.ListWorkMapFeaturesRequest]) (*connect.Response[openv1.ListWorkMapFeaturesResponse], error) {
	features, err := h.works.ListMapFeatures(ctx, ListWorkMapFeaturesQuery{Viewport: viewportOf(req.Msg.Viewport), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts})
	return render(features, err, mapFeaturesMessage)
}

func viewportOf(viewport *openv1.WorkMapViewport) *Viewport {
	if viewport == nil {
		return nil
	}
	converted := &Viewport{
		Zoom: viewport.Zoom, WidthPx: viewport.WidthPx, HeightPx: viewport.HeightPx,
		ClusterRadiusPx: viewport.ClusterRadiusPx, MinClusterPoints: viewport.MinClusterPoints,
	}
	if bounds := viewport.Bounds; bounds != nil {
		converted.Bounds = &Bounds{West: bounds.West, South: bounds.South, East: bounds.East, North: bounds.North}
	}
	return converted
}

func mapFeaturesMessage(features MapFeatures) *openv1.ListWorkMapFeaturesResponse {
	response := &openv1.ListWorkMapFeaturesResponse{}
	if features.Clusters != nil {
		response.Clusters = make([]*openv1.WorkMapCluster, 0, len(features.Clusters))
		for _, cluster := range features.Clusters {
			response.Clusters = append(response.Clusters, &openv1.WorkMapCluster{
				Id: cluster.ID, Lat: cluster.Lat, Lng: cluster.Lng, PlaceCount: cluster.PlaceCount, WorkCount: cluster.WorkCount,
				MinBreakoutZoom: cluster.MinBreakoutZoom,
				Bounds:          &openv1.WorkMapBounds{West: cluster.Bounds.West, South: cluster.Bounds.South, East: cluster.Bounds.East, North: cluster.Bounds.North},
			})
		}
	}
	if features.Items != nil {
		response.Items = make([]*openv1.WorkMapItem, 0, len(features.Items))
		for _, item := range features.Items {
			response.Items = append(response.Items, &openv1.WorkMapItem{
				PlaceId: item.PlaceID, Name: item.Name, Address: item.Address, Lat: item.Lat, Lng: item.Lng,
				WorkCount: item.WorkCount, PrimaryWorkId: item.PrimaryWorkID, PrimaryWorkSlug: item.PrimaryWorkSlug,
				PrimaryWorkTitle: item.PrimaryWorkTitle,
			})
		}
	}
	return response
}

// workStatusToProto maps a stored Work status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func workStatusToProto(status workdomain.Status) openv1.WorkStatus {
	switch status {
	case workdomain.StatusDraft:
		return openv1.WorkStatus_WORK_STATUS_DRAFT
	case workdomain.StatusPublished:
		return openv1.WorkStatus_WORK_STATUS_PUBLISHED
	case workdomain.StatusArchived:
		return openv1.WorkStatus_WORK_STATUS_ARCHIVED
	default:
		return openv1.WorkStatus_WORK_STATUS_UNSPECIFIED
	}
}

func summaryMessage(view Summary) *openv1.WorkSummary {
	work := view.Work
	summary := &openv1.WorkSummary{
		Id:        work.ID,
		Title:     view.Title,
		Type:      openv1.WorkType(openv1.WorkType_value[work.Type]),
		Year:      work.Year,
		Month:     work.Month,
		IsPresent: work.IsPresent,
		Featured:  work.Featured,
		Status:    workStatusToProto(work.Status),
	}
	if work.Slug != nil {
		summary.Slug = work.Slug
	}
	if view.Summary != nil {
		summary.Summary = view.Summary
	}
	if work.PublishedAt != nil {
		summary.PublishedAt = timestamppb.New(*work.PublishedAt)
	}
	if work.MapPlaceID != nil {
		summary.MapPlaceId = work.MapPlaceID
	}
	if work.UntilYear != nil {
		summary.UntilYear = work.UntilYear
	}
	if work.UntilMonth != nil {
		summary.UntilMonth = work.UntilMonth
	}
	if view.FeaturedImage != nil {
		summary.FeaturedImageAsset = mediaasset.ReferenceProto(view.FeaturedImage)
	}
	return summary
}

// publicWorkMetadata is the Work's metadata, or none when it cannot be
// converted.
func publicWorkMetadata(ctx context.Context, work workdomain.Work) *structpb.Struct {
	if work.Metadata == nil {
		return nil
	}
	metadata, err := structpb.NewStruct(work.Metadata)
	if err != nil {
		slog.ErrorContext(ctx, "Public Work metadata conversion failed; serving work without metadata", "domain", "work", "operation", "convert_public_work_metadata", failurecause.Attr(err), "work_id", work.ID)
		return nil
	}
	return metadata
}

func workMessage(ctx context.Context, detail Detail) *openv1.Work {
	work := detail.Work.Work
	protoWork := &openv1.Work{
		Id:        work.ID,
		Title:     detail.Title,
		Type:      openv1.WorkType(openv1.WorkType_value[work.Type]),
		Year:      work.Year,
		Month:     work.Month,
		IsPresent: work.IsPresent,
		Featured:  work.Featured,
		Status:    workStatusToProto(work.Status),
		CreatedAt: timestamppb.New(work.CreatedAt),
		UpdatedAt: timestamppb.New(work.UpdatedAt),
		Document:  detail.Document,
		Revision:  detail.Revision,
		Slug:      work.Slug,
		Summary:   detail.Summary,
	}
	protoWork.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(detail.Localization)
	if work.PublishedAt != nil {
		protoWork.PublishedAt = timestamppb.New(*work.PublishedAt)
	}
	if work.MapPlaceID != nil {
		protoWork.MapPlaceId = work.MapPlaceID
		protoWork.LocationPlace = mapPlaceOf(detail.Work)
	}
	protoWork.UntilYear, protoWork.UntilMonth = work.UntilYear, work.UntilMonth
	protoWork.OgAsset = mediaasset.ReferenceProto(detail.OgAsset)
	protoWork.FeaturedImageAsset = mediaasset.ReferenceProto(detail.FeaturedImage)
	protoWork.Metadata = publicWorkMetadata(ctx, work)
	protoWork.CreditGroups = make([]*openv1.WorkCreditGroup, 0, len(detail.CreditGroups))
	for _, group := range detail.CreditGroups {
		protoWork.CreditGroups = append(protoWork.CreditGroups, &openv1.WorkCreditGroup{Id: group.ID, Name: group.Name})
	}
	protoWork.Credits = make([]*openv1.WorkCredit, 0, len(detail.Credits))
	for _, view := range detail.Credits {
		credit := view.Credit
		message := &openv1.WorkCredit{Id: credit.ID}
		if credit.GroupID != nil {
			message.GroupId = credit.GroupID
		}
		if credit.Name != nil {
			message.Name = credit.Name
		}
		if credit.CreditRole != nil {
			message.CreditRole = credit.CreditRole
		}
		if artist := view.Artist; artist != nil {
			message.Artist = &openv1.CreditArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug, ImageAsset: mediaasset.ReferenceProto(artist.Image)}
		}
		if credit.MemberID != nil {
			message.Member = memberref.ReferenceMessage(view.Member)
		}
		protoWork.Credits = append(protoWork.Credits, message)
	}
	if detail.Clients != nil {
		protoWork.Clients = make([]*openv1.WorkClient, 0, len(detail.Clients))
		for _, client := range detail.Clients {
			message := &openv1.WorkClient{Id: client.ID, Name: client.Name, LogoLightAsset: mediaasset.ReferenceProto(client.LightLogo), LogoDarkAsset: mediaasset.ReferenceProto(client.DarkLogo)}
			if client.Website != nil {
				message.Website = client.Website
			}
			protoWork.Clients = append(protoWork.Clients, message)
		}
	}
	return protoWork
}
