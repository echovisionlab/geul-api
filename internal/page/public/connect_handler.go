package public

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the public Page RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedPageServiceHandler
	pages *Service
}

// NewConnectHandler returns the Connect adapter of pages.
func NewConnectHandler(pages *Service) *ConnectHandler {
	return &ConnectHandler{pages: pages}
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetPageRequest]) (*connect.Response[openv1.GetPageResponse], error) {
	read, err := h.pages.GetPage(ctx, GetPageQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.ShareToken, SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	response, err := getPageResponse(read)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(response), nil
}

func getPageResponse(read Read) (*openv1.GetPageResponse, error) {
	response := &openv1.GetPageResponse{AccessReason: commonv1.PageAccessReason(read.AccessReason)}
	if !read.Visible {
		return response, nil
	}
	p := read.Page
	policy, err := pageaccess.Decode(p.AccessPolicy)
	if err != nil {
		return nil, err
	}
	documentLayout, err := page.DocumentLayoutMessage(p.DocumentLayout)
	if err != nil {
		return nil, err
	}
	message := &openv1.Page{
		AccessPolicy:          pageaccess.Message(policy),
		Id:                    p.ID,
		Title:                 read.Title,
		ShowTitle:             p.ShowTitle,
		Status:                toProtoStatus(p.Status),
		Document:              read.Document,
		Revision:              read.Revision,
		DocumentLayout:        documentLayout,
		CreatedAt:             timestamppb.New(p.CreatedAt),
		UpdatedAt:             timestamppb.New(p.UpdatedAt),
		Slug:                  p.Slug,
		Summary:               read.Summary,
		LocalizationInfo:      publiccontent.ToProtoLocalizationInfo(read.Localization),
		OgAsset:               mediaasset.ReferenceProto(read.OgAsset),
		FeaturedImageDelivery: fileref.DeliveryProto(read.FeaturedImage),
	}
	if p.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*p.PublishedAt)
	}
	response.Page = message
	response.BlockMedia = read.BlockMedia
	return response, nil
}

// toProtoStatus maps a stored Page status to the public API enum. A value
// the API does not know is UNSPECIFIED.
func toProtoStatus(status page.Status) openv1.PageStatus {
	switch status {
	case page.StatusDraft:
		return openv1.PageStatus_PAGE_STATUS_DRAFT
	case page.StatusPublished:
		return openv1.PageStatus_PAGE_STATUS_PUBLISHED
	default:
		return openv1.PageStatus_PAGE_STATUS_UNSPECIFIED
	}
}
