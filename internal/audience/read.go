package audience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
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

// GetSegmentQuery reads one segment.
type GetSegmentQuery struct {
	ID string
}

// GetSegment returns the segment with its configuration, reference counts
// and, when the counter answers, its estimated recipients.
func (s *Service) GetSegment(ctx context.Context, query GetSegmentQuery) (*managev1.Segment, error) {
	can, err := policyv1.AudienceSegment.View(query.ID)
	if err != nil {
		return nil, err
	}
	if err := requireAudienceAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	var segment model.AudienceSegment
	if err := s.db.WithContext(ctx).First(&segment, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errSegmentNotFound()
		}
		return nil, err
	}
	if err := LoadSegmentConfig(ctx, s.db, &segment); err != nil {
		return nil, err
	}
	if err := s.loadSegmentReferenceCounts(ctx, s.db, []*model.AudienceSegment{&segment}); err != nil {
		return nil, err
	}
	proto := toProtoSegment(&segment)
	if count, err := s.estimateCount(ctx, &segment); err == nil {
		proto.EstimatedCount = &count
	} else {
		slog.ErrorContext(ctx, "Audience segment count estimate failed; returning segment without a count", "domain", "audience", "event", "audience.operation_failed", "operation", "estimate_segment_count", "outcome", "failed", "segment_id", segment.ID, failurecause.Attr(err))
	}
	return proto, nil
}

// ListSegmentsQuery pages through segments for administrators.
type ListSegmentsQuery struct {
	IncludeArchived bool
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// SegmentPage is one page of segments.
type SegmentPage struct {
	Segments   []*managev1.Segment
	Pagination *commonv1.PaginationResponse
}

// ListSegments returns one page of segments with their configuration and
// reference counts.
func (s *Service) ListSegments(ctx context.Context, query ListSegmentsQuery) (SegmentPage, error) {
	can, err := policyv1.AudienceSegment.List()
	if err != nil {
		return SegmentPage{}, err
	}
	if err := requireAudienceAdminCan(ctx, s.spiceDB, can); err != nil {
		return SegmentPage{}, err
	}
	list := s.db.WithContext(ctx).Model(&model.AudienceSegment{})
	if !query.IncludeArchived {
		list = list.Where("archived_at IS NULL")
	}
	list, err = segmentFilterConfig.ApplyFilters(list, query.Filters)
	if err != nil {
		return SegmentPage{}, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return SegmentPage{}, err
	}
	limit, offset := normalizePagination(query.Limit, query.Offset)
	list, err = segmentSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return SegmentPage{}, err
	}
	var segments []model.AudienceSegment
	if err := list.Limit(int(limit)).Offset(int(offset)).Find(&segments).Error; err != nil {
		return SegmentPage{}, err
	}
	pointers := make([]*model.AudienceSegment, 0, len(segments))
	for i := range segments {
		pointers = append(pointers, &segments[i])
	}
	if err := LoadSegmentConfigs(ctx, s.db, pointers); err != nil {
		return SegmentPage{}, err
	}
	if err := s.loadSegmentReferenceCounts(ctx, s.db, pointers); err != nil {
		return SegmentPage{}, err
	}
	protoSegments := make([]*managev1.Segment, len(segments))
	for i := range segments {
		protoSegments[i] = toProtoSegment(&segments[i])
	}
	return SegmentPage{
		Segments:   protoSegments,
		Pagination: (queryutil.Pagination{Limit: limit, Offset: offset}).BuildResponse(total),
	}, nil
}

// ListAuthenticatedAccessSegmentsQuery pages through the active segments a
// download policy may grant access to.
type ListAuthenticatedAccessSegmentsQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// AccessSegmentPage is one page of segment summaries.
type AccessSegmentPage struct {
	Segments   []*managev1.AudienceSegmentSummary
	Pagination *commonv1.PaginationResponse
}

// ListAuthenticatedAccessSegments returns summaries of the segments that can
// grant authenticated download access, for authors editing a policy.
func (s *Service) ListAuthenticatedAccessSegments(ctx context.Context, query ListAuthenticatedAccessSegmentsQuery) (AccessSegmentPage, error) {
	if err := requireDownloadPolicyAuthor(ctx, s.spiceDB); err != nil {
		return AccessSegmentPage{}, err
	}
	list := s.db.WithContext(ctx).
		Model(&model.AudienceSegment{}).
		Where("segment_type IN ?", authenticatedAccessSegmentTypes).
		Where("archived_at IS NULL")
	var err error
	list, err = segmentFilterConfig.ApplyFilters(list, query.Filters)
	if err != nil {
		return AccessSegmentPage{}, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return AccessSegmentPage{}, err
	}
	list, err = segmentSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return AccessSegmentPage{}, err
	}
	limit, offset := normalizePagination(query.Limit, query.Offset)
	var segments []model.AudienceSegment
	if err := list.Limit(int(limit)).Offset(int(offset)).Find(&segments).Error; err != nil {
		return AccessSegmentPage{}, err
	}
	summaries := make([]*managev1.AudienceSegmentSummary, 0, len(segments))
	for i := range segments {
		summary, valid := AuthenticatedAccessSegmentSummary(&segments[i])
		if !valid {
			return AccessSegmentPage{}, errors.New("authenticated access segment query returned an ineligible segment")
		}
		summaries = append(summaries, summary)
	}
	return AccessSegmentPage{
		Segments: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: int64(offset+limit) < total,
		},
	}, nil
}

func (s *Service) estimateCount(ctx context.Context, segment *model.AudienceSegment) (int32, error) {
	if segment == nil {
		return 0, fmt.Errorf("segment is required")
	}
	switch segment.SegmentType {
	case model.AudienceSegmentAllMembers, model.AudienceSegmentMemberTags, model.AudienceSegmentMembersByFilter:
	default:
		return 0, fmt.Errorf("unknown segment type: %s", segment.SegmentType)
	}
	count, err := s.recipientCounter.Count(ctx, segment)
	if err != nil {
		return 0, err
	}
	return int32(count), nil
}

func (s *Service) loadSegmentForResponse(ctx context.Context, db *gorm.DB, segmentID string, result *model.AudienceSegment) error {
	*result = model.AudienceSegment{}
	if err := db.WithContext(ctx).First(result, "id = ?", segmentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSegmentNotFound()
		}
		return err
	}
	if err := LoadSegmentConfig(ctx, db, result); err != nil {
		return err
	}
	return s.loadSegmentReferenceCounts(ctx, db, []*model.AudienceSegment{result})
}

func toProtoSegment(segment *model.AudienceSegment) *managev1.Segment {
	proto := &managev1.Segment{
		Id:                           segment.ID,
		Name:                         segment.Name,
		Description:                  segment.Description,
		SegmentType:                  segmentTypeToProto(segment.SegmentType),
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

func normalizePagination(requestedLimit, requestedOffset int32) (int32, int32) {
	limit, offset := int32(20), int32(0)
	if requestedLimit > 0 {
		limit = requestedLimit
	}
	if requestedOffset > 0 {
		offset = requestedOffset
	}
	if limit > 100 {
		limit = 100
	}
	return limit, offset
}

func errSegmentNotFound() error {
	return domainerr.New(domainerr.NotFound, "", "segment not found")
}
