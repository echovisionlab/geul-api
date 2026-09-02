package public

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// TermsService implements the public TermsService
type TermsService struct {
	openv1connect.UnimplementedTermsServiceHandler
	db            *gorm.DB
	contentBlocks *contentblock.Store
	media         legaldomain.PublicMedia
}

// NewTermsService creates a new public TermsService
func NewTermsService(db *gorm.DB, media legaldomain.PublicMedia) *TermsService {
	return &TermsService{db: db, media: media}
}

// NewTermsServiceWithContentBlocks creates a public TermsService backed by the
// canonical typed Block store.
func NewTermsServiceWithContentBlocks(
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	media legaldomain.PublicMedia,
) *TermsService {
	service := NewTermsService(db, media)
	service.contentBlocks = contentBlocks
	return service
}

// Get returns the currently active terms of service, or a specific version by ID
func (s *TermsService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetTermsRequest],
) (*connect.Response[openv1.GetTermsResponse], error) {
	result, err := getPublicLegalDocument(ctx, s.db, s.contentBlocks, s.media, s.publicLegalSpec(), publicLegalRequest{
		requestedID:    optionalStringValue(req.Msg.Id),
		shareToken:     optionalStringValue(req.Msg.ShareToken),
		sharePassword:  optionalStringValue(req.Msg.SharePassword),
		acceptLanguage: req.Header().Get("Accept-Language"),
		now:            time.Now(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.GetTermsResponse{
		Terms:     result.current,
		Scheduled: result.scheduled,
	}), nil
}

func (s *TermsService) publicLegalSpec() publicLegalSpec[model.Terms, openv1.Terms] {
	return publicLegalSpec[model.Terms, openv1.Terms]{
		entityName:      "terms",
		shareEntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS,
		activeStatus:    managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
		archivedStatus:  managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
		scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
		id:              func(document *model.Terms) string { return document.ID },
		title:           func(document *model.Terms) string { return document.Title },
		setTitle:        func(document *model.Terms, value string) { document.Title = value },
		toProto:         s.toProto,
		toScheduled: func(document *model.Terms) *openv1.Terms {
			result := &openv1.Terms{
				Id: document.ID, Version: int32(document.Version),
				Title: resolveCanonicalLegalPublicTitle(document.Title, nil),
			}
			if document.EffectiveFrom != nil {
				result.EffectiveFrom = timestamppb.New(*document.EffectiveFrom)
			}
			if document.EffectiveUntil != nil {
				result.EffectiveUntil = timestamppb.New(*document.EffectiveUntil)
			}
			return result
		},
	}
}

// List returns archived terms of service
func (s *TermsService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListTermsRequest],
) (*connect.Response[openv1.ListTermsResponse], error) {
	limit := req.Msg.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	offset := max(req.Msg.Offset, 0)

	var total int64
	s.db.WithContext(ctx).Model(&model.Terms{}).
		Where("status = ?", managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()).
		Count(&total)

	var items []model.Terms
	err := s.db.WithContext(ctx).
		Where("status = ?", managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()).
		Order("effective_from DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&items).Error

	if err != nil {
		return nil, errs.Internal(err)
	}

	summaries := make([]*openv1.TermsSummary, 0, len(items))
	for _, item := range items {
		summary := &openv1.TermsSummary{
			Id:      item.ID,
			Version: int32(item.Version),
			Title:   resolveCanonicalLegalPublicTitle(item.Title, nil),
		}
		if item.EffectiveFrom != nil {
			summary.EffectiveFrom = timestamppb.New(*item.EffectiveFrom)
		}
		if item.EffectiveUntil != nil {
			summary.EffectiveUntil = timestamppb.New(*item.EffectiveUntil)
		}
		summaries = append(summaries, summary)
	}

	return connect.NewResponse(&openv1.ListTermsResponse{
		Items: summaries,
		Total: int32(total),
	}), nil
}

func (s *TermsService) toProto(
	t *model.Terms,
	localization *publiccontent.Selection,
	ogAsset *commonv1.AssetRef,
	content publicLegalContentProjection,
) *openv1.Terms {
	proto := &openv1.Terms{
		Id:       t.ID,
		Version:  int32(t.Version),
		Title:    t.Title,
		Document: content.document,
		Revision: content.revision,
	}
	if t.EffectiveFrom != nil {
		proto.EffectiveFrom = timestamppb.New(*t.EffectiveFrom)
	}
	if localization != nil {
		proto.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(*localization)
	}
	if t.EffectiveUntil != nil {
		proto.EffectiveUntil = timestamppb.New(*t.EffectiveUntil)
	}
	proto.OgAsset = ogAsset
	return proto
}
