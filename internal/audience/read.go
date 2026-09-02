package audience

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var segmentSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         "name",
		"created_at":   "created_at",
		"segment_type": "segment_type",
	},
	DefaultSort: "name ASC, id ASC",
}

var segmentFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name"},
		},
	},
}

func (s *AudienceService) GetSegment(
	ctx context.Context,
	req *connect.Request[managev1.GetSegmentRequest],
) (*connect.Response[managev1.Segment], error) {
	can, err := policyv1.AudienceSegment.View(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := requireAudienceAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	var segment model.AudienceSegment
	if err := s.db.WithContext(ctx).First(&segment, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("segment not found")
		}
		return nil, errs.Internal(err)
	}
	if err := LoadSegmentConfig(ctx, s.db, &segment); err != nil {
		return nil, errs.Internal(err)
	}
	if err := loadSegmentReferenceCounts(ctx, s.db, []*model.AudienceSegment{&segment}); err != nil {
		return nil, errs.Internal(err)
	}
	proto := toProtoSegment(&segment)
	if count, err := s.estimateCount(ctx, &segment); err == nil {
		proto.EstimatedCount = &count
	}
	return connect.NewResponse(proto), nil
}

func (s *AudienceService) ListSegmentsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListSegmentsAdminRequest],
) (*connect.Response[managev1.ListSegmentsAdminResponse], error) {
	can, err := policyv1.AudienceSegment.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := requireAudienceAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).Model(&model.AudienceSegment{})
	if !req.Msg.IncludeArchived {
		query = query.Where("archived_at IS NULL")
	}
	query, err = segmentFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	limit, offset := normalizePagination(req.Msg.Pagination)
	query, err = segmentSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	var segments []model.AudienceSegment
	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&segments).Error; err != nil {
		return nil, errs.Internal(err)
	}
	pointers := make([]*model.AudienceSegment, 0, len(segments))
	for i := range segments {
		pointers = append(pointers, &segments[i])
	}
	if err := LoadSegmentConfigs(ctx, s.db, pointers); err != nil {
		return nil, errs.Internal(err)
	}
	if err := loadSegmentReferenceCounts(ctx, s.db, pointers); err != nil {
		return nil, errs.Internal(err)
	}
	protoSegments := make([]*managev1.Segment, len(segments))
	for i := range segments {
		protoSegments[i] = toProtoSegment(&segments[i])
	}
	return connect.NewResponse(&managev1.ListSegmentsAdminResponse{
		Segments:   protoSegments,
		Pagination: &commonv1.PaginationResponse{Total: int32(total), Limit: limit, Offset: offset},
	}), nil
}

func (s *AudienceService) ListSegmentsForAuthenticatedAccess(
	ctx context.Context,
	req *connect.Request[managev1.ListSegmentsForAuthenticatedAccessRequest],
) (*connect.Response[managev1.ListSegmentsForAuthenticatedAccessResponse], error) {
	if _, err := requireDownloadPolicyAuthor(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).
		Model(&model.AudienceSegment{}).
		Where("segment_type IN ?", authenticatedAccessSegmentTypes).
		Where("archived_at IS NULL")
	var err error
	query, err = segmentFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query, err = segmentSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	limit, offset := normalizePagination(req.Msg.Pagination)
	var segments []model.AudienceSegment
	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&segments).Error; err != nil {
		return nil, errs.Internal(err)
	}
	summaries := make([]*managev1.AudienceSegmentSummary, 0, len(segments))
	for i := range segments {
		summary, valid := AuthenticatedAccessSegmentSummary(&segments[i])
		if !valid {
			return nil, errs.InternalMsg("authenticated access segment query returned an ineligible segment")
		}
		summaries = append(summaries, summary)
	}
	return connect.NewResponse(newAudienceSegmentListResponse(summaries, total, limit, offset)), nil
}

func (s *AudienceService) estimateCount(ctx context.Context, segment *model.AudienceSegment) (int32, error) {
	if segment == nil {
		return 0, fmt.Errorf("segment is required")
	}
	switch segment.SegmentType {
	case managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS.String(),
		managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String(),
		managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String():
	default:
		return 0, fmt.Errorf("unknown segment type: %s", segment.SegmentType)
	}
	count, err := s.recipientCounter.Count(ctx, segment)
	if err != nil {
		return 0, err
	}
	return int32(count), nil
}

func loadSegmentForResponse(ctx context.Context, db *gorm.DB, segmentID string, result *model.AudienceSegment) error {
	*result = model.AudienceSegment{}
	if err := db.WithContext(ctx).First(result, "id = ?", segmentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFoundMsg("segment not found")
		}
		return errs.Internal(err)
	}
	if err := LoadSegmentConfig(ctx, db, result); err != nil {
		return errs.Internal(err)
	}
	if err := loadSegmentReferenceCounts(ctx, db, []*model.AudienceSegment{result}); err != nil {
		return errs.Internal(err)
	}
	return nil
}

func toProtoSegment(segment *model.AudienceSegment) *managev1.Segment {
	proto := &managev1.Segment{
		Id:                           segment.ID,
		Name:                         segment.Name,
		Description:                  segment.Description,
		SegmentType:                  managev1.SegmentType(managev1.SegmentType_value[segment.SegmentType]),
		Config:                       toProtoSegmentConfig(segment.Config),
		CreatedAt:                    timestamppb.New(segment.CreatedAt),
		CampaignCount:                segment.CampaignCount,
		DeliveryRunCount:             segment.DeliveryRunCount,
		DownloadPolicyReferenceCount: segment.DownloadPolicyReferenceCount,
	}
	if segment.UpdatedAt != nil {
		proto.UpdatedAt = timestamppb.New(*segment.UpdatedAt)
	}
	if segment.ArchivedAt != nil {
		proto.ArchivedAt = timestamppb.New(*segment.ArchivedAt)
	}
	return proto
}

func toProtoSegmentConfig(config model.AudienceSegmentConfig) *managev1.SegmentConfig {
	roles := make([]policyv1.AuthorizationRole, 0, len(config.AccountRoles))
	for _, role := range config.AccountRoles {
		switch role {
		case policyv1.Role.Admin().ID():
			roles = append(roles, policyv1.AuthorizationRole_ADMIN)
		case policyv1.Role.Author().ID():
			roles = append(roles, policyv1.AuthorizationRole_AUTHOR)
		case policyv1.Role.User().ID():
			roles = append(roles, policyv1.AuthorizationRole_USER)
		}
	}
	return &managev1.SegmentConfig{
		MemberTagIds:     config.MemberTagIDs,
		AccountRoles:     roles,
		CreatedAfter:     timestampOrNil(config.CreatedAfter),
		CreatedBefore:    timestampOrNil(config.CreatedBefore),
		ExcludeMemberIds: config.ExcludeMemberIDs,
	}
}

func requireAudienceAdminCan(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	can policyv1.Can,
) error {
	return authz.RequireAdminCan(ctx, spiceDB, can)
}

func normalizePagination(pagination *commonv1.PaginationRequest) (int32, int32) {
	limit, offset := int32(20), int32(0)
	if pagination != nil {
		if pagination.Limit > 0 {
			limit = pagination.Limit
		}
		if pagination.Offset > 0 {
			offset = pagination.Offset
		}
	}
	if limit > 100 {
		limit = 100
	}
	return limit, offset
}

func newAudienceSegmentListResponse(
	summaries []*managev1.AudienceSegmentSummary,
	total int64,
	limit, offset int32,
) *managev1.ListSegmentsForAuthenticatedAccessResponse {
	return &managev1.ListSegmentsForAuthenticatedAccessResponse{
		Segments: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: int64(offset+limit) < total,
		},
	}
}
