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

// PrivacyService implements the public PrivacyService
type PrivacyService struct {
	openv1connect.UnimplementedPrivacyServiceHandler
	db            *gorm.DB
	contentBlocks *contentblock.Store
	media         legaldomain.PublicMedia
}

// NewPrivacyService creates a new public PrivacyService
func NewPrivacyService(db *gorm.DB, media legaldomain.PublicMedia) *PrivacyService {
	return &PrivacyService{db: db, media: media}
}

// NewPrivacyServiceWithContentBlocks creates a public PrivacyService backed by
// the canonical typed Block store.
func NewPrivacyServiceWithContentBlocks(
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	media legaldomain.PublicMedia,
) *PrivacyService {
	service := NewPrivacyService(db, media)
	service.contentBlocks = contentBlocks
	return service
}

// Get returns the currently active privacy policy, or a specific version by ID
func (s *PrivacyService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetPrivacyRequest],
) (*connect.Response[openv1.GetPrivacyResponse], error) {
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
	return connect.NewResponse(&openv1.GetPrivacyResponse{
		Privacy:   result.current,
		Scheduled: result.scheduled,
	}), nil
}

func (s *PrivacyService) publicLegalSpec() publicLegalSpec[model.Privacy, openv1.Privacy] {
	return publicLegalSpec[model.Privacy, openv1.Privacy]{
		entityName:      "privacy",
		shareEntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY,
		activeStatus:    managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
		archivedStatus:  managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
		scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
		id:              func(document *model.Privacy) string { return document.ID },
		title:           func(document *model.Privacy) string { return document.Title },
		setTitle:        func(document *model.Privacy, value string) { document.Title = value },
		toProto:         s.toProto,
		toScheduled: func(document *model.Privacy) *openv1.Privacy {
			result := &openv1.Privacy{
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

// List returns archived privacy policies
func (s *PrivacyService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListPrivacyRequest],
) (*connect.Response[openv1.ListPrivacyResponse], error) {
	limit := req.Msg.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	offset := max(req.Msg.Offset, 0)

	var total int64
	s.db.WithContext(ctx).Model(&model.Privacy{}).
		Where("status = ?", managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()).
		Count(&total)

	var items []model.Privacy
	err := s.db.WithContext(ctx).
		Where("status = ?", managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()).
		Order("effective_from DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&items).Error

	if err != nil {
		return nil, errs.Internal(err)
	}

	summaries := make([]*openv1.PrivacySummary, 0, len(items))
	for _, item := range items {
		summary := &openv1.PrivacySummary{
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

	return connect.NewResponse(&openv1.ListPrivacyResponse{
		Items: summaries,
		Total: int32(total),
	}), nil
}

func (s *PrivacyService) toProto(
	p *model.Privacy,
	localization *publiccontent.Selection,
	ogAsset *commonv1.AssetRef,
	content publicLegalContentProjection,
) *openv1.Privacy {
	proto := &openv1.Privacy{
		Id:       p.ID,
		Version:  int32(p.Version),
		Title:    p.Title,
		Document: content.document,
		Revision: content.revision,
	}
	if p.EffectiveFrom != nil {
		proto.EffectiveFrom = timestamppb.New(*p.EffectiveFrom)
	}
	if localization != nil {
		proto.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(*localization)
	}
	if p.EffectiveUntil != nil {
		proto.EffectiveUntil = timestamppb.New(*p.EffectiveUntil)
	}
	proto.OgAsset = ogAsset
	return proto
}
