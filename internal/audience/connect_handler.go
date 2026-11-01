package audience

import (
	"context"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the Audience Connect RPCs. Each method translates the
// request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedAudienceServiceHandler
	audiences *Service
}

// NewConnectHandler returns the Connect adapter of audiences.
func NewConnectHandler(audiences *Service) *ConnectHandler {
	return &ConnectHandler{audiences: audiences}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func respondSegment(view SegmentView, err error) (*connect.Response[managev1.Segment], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(segmentMessage(view)), nil
}

// segmentConfigInput translates a requested filter. An API role the domain
// does not know becomes an empty role ID and a malformed timestamp is named,
// so validation rejects both in its own order.
func segmentConfigInput(config *managev1.SegmentConfig) *SegmentConfigInput {
	if config == nil {
		return nil
	}
	input := &SegmentConfigInput{
		MemberTagIDs:     config.MemberTagIds,
		AccountRoles:     make([]string, 0, len(config.AccountRoles)),
		ExcludeMemberIDs: config.ExcludeMemberIds,
	}
	for _, role := range config.AccountRoles {
		switch role {
		case policyv1.AuthorizationRole_ADMIN:
			input.AccountRoles = append(input.AccountRoles, policyv1.Role.Admin().ID())
		case policyv1.AuthorizationRole_AUTHOR:
			input.AccountRoles = append(input.AccountRoles, policyv1.Role.Author().ID())
		case policyv1.AuthorizationRole_USER:
			input.AccountRoles = append(input.AccountRoles, policyv1.Role.User().ID())
		default:
			input.AccountRoles = append(input.AccountRoles, "")
		}
	}
	if config.CreatedAfter != nil {
		value := config.CreatedAfter.AsTime()
		input.CreatedAfter = &value
		if config.CreatedAfter.CheckValid() != nil {
			input.malformedTime = "created_after"
		}
	}
	if config.CreatedBefore != nil {
		value := config.CreatedBefore.AsTime()
		input.CreatedBefore = &value
		if config.CreatedBefore.CheckValid() != nil && input.malformedTime == "" {
			input.malformedTime = "created_before"
		}
	}
	return input
}

func (h *ConnectHandler) CreateSegment(ctx context.Context, req *connect.Request[managev1.CreateSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	return respondSegment(h.audiences.CreateSegment(ctx, CreateSegmentCommand{
		Name: req.Msg.Name, Description: req.Msg.Description,
		SegmentType: segmentTypeFromProto(req.Msg.SegmentType), Config: segmentConfigInput(req.Msg.Config),
	}))
}

func (h *ConnectHandler) EstimateSegmentCount(ctx context.Context, req *connect.Request[managev1.EstimateSegmentCountRequest]) (*connect.Response[managev1.EstimateSegmentCountResponse], error) {
	count, err := h.audiences.EstimateSegmentCount(ctx, EstimateSegmentCountQuery{
		SegmentType: segmentTypeFromProto(req.Msg.SegmentType), Config: segmentConfigInput(req.Msg.Config),
	})
	return respond(&managev1.EstimateSegmentCountResponse{Count: count}, err)
}

func (h *ConnectHandler) UpdateSegment(ctx context.Context, req *connect.Request[managev1.UpdateSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	command := UpdateSegmentCommand{
		ID: req.Msg.Id, Name: req.Msg.Name, Description: req.Msg.Description, Config: segmentConfigInput(req.Msg.Config),
	}
	if req.Msg.SegmentType != nil {
		segmentType := segmentTypeFromProto(*req.Msg.SegmentType)
		command.SegmentType = &segmentType
	}
	if observed := req.Msg.Observed; observed != nil {
		command.Observed = &ObservedSegment{
			SegmentType: segmentTypeFromProto(observed.SegmentType), Config: segmentConfigInput(observed.Config),
		}
	}
	return respondSegment(h.audiences.UpdateSegment(ctx, command))
}

func (h *ConnectHandler) ArchiveSegment(ctx context.Context, req *connect.Request[managev1.ArchiveSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	return respondSegment(h.audiences.ArchiveSegment(ctx, ArchiveSegmentCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) RestoreSegment(ctx context.Context, req *connect.Request[managev1.RestoreSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	return respondSegment(h.audiences.RestoreSegment(ctx, RestoreSegmentCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) GetSegment(ctx context.Context, req *connect.Request[managev1.GetSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	return respondSegment(h.audiences.GetSegment(ctx, GetSegmentQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListSegmentsAdmin(ctx context.Context, req *connect.Request[managev1.ListSegmentsAdminRequest]) (*connect.Response[managev1.ListSegmentsAdminResponse], error) {
	page, err := h.audiences.ListSegments(ctx, ListSegmentsQuery{
		IncludeArchived: req.Msg.IncludeArchived,
		Limit:           req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	segments := make([]*managev1.Segment, len(page.Segments))
	for i, view := range page.Segments {
		segments[i] = segmentMessage(view)
	}
	return connect.NewResponse(&managev1.ListSegmentsAdminResponse{
		Segments:   segments,
		Pagination: (queryutil.Pagination{Limit: page.Limit, Offset: page.Offset}).BuildResponse(page.Total),
	}), nil
}

func (h *ConnectHandler) ListSegmentsForAuthenticatedAccess(ctx context.Context, req *connect.Request[managev1.ListSegmentsForAuthenticatedAccessRequest]) (*connect.Response[managev1.ListSegmentsForAuthenticatedAccessResponse], error) {
	page, err := h.audiences.ListAuthenticatedAccessSegments(ctx, ListAuthenticatedAccessSegmentsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	summaries := make([]*managev1.AudienceSegmentSummary, len(page.Segments))
	for i, segment := range page.Segments {
		summaries[i] = segmentSummary(segment)
	}
	return connect.NewResponse(&managev1.ListSegmentsForAuthenticatedAccessResponse{
		Segments: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(page.Total),
			Limit:   page.Limit,
			Offset:  page.Offset,
			HasMore: int64(page.Offset+page.Limit) < page.Total,
		},
	}), nil
}

func segmentMessage(view SegmentView) *managev1.Segment {
	segment := view.Segment
	message := &managev1.Segment{
		Id:                           segment.ID,
		Name:                         segment.Name,
		Description:                  segment.Description,
		SegmentType:                  segmentTypeToProto(segment.Type),
		Config:                       segmentConfigMessage(segment.Config),
		CreatedAt:                    timestamppb.New(segment.CreatedAt),
		CampaignCount:                view.References.Campaigns,
		DeliveryRunCount:             view.References.DeliveryRuns,
		DownloadPolicyReferenceCount: view.References.DownloadPolicies,
		EstimatedCount:               view.EstimatedCount,
	}
	if segment.UpdatedAt != nil {
		message.UpdatedAt = timestamppb.New(*segment.UpdatedAt)
	}
	if segment.ArchivedAt != nil {
		message.ArchivedAt = timestamppb.New(*segment.ArchivedAt)
	}
	return message
}

func segmentConfigMessage(config Config) *managev1.SegmentConfig {
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

func timestampOrNil(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

// AuthenticatedAccessSegmentSummary returns the summary restricted download
// policy APIs expose of a segment, when it can grant authenticated access.
func AuthenticatedAccessSegmentSummary(segment Segment) (*managev1.AudienceSegmentSummary, bool) {
	if !segment.GrantsAuthenticatedAccess() {
		return nil, false
	}
	return segmentSummary(segment), true
}

func segmentSummary(segment Segment) *managev1.AudienceSegmentSummary {
	return &managev1.AudienceSegmentSummary{
		Id:          segment.ID,
		Name:        segment.Name,
		Description: segment.Description,
		SegmentType: segmentTypeToProto(segment.Type.trimmed()),
	}
}

// segmentTypeToProto maps a stored segment type to the API enum. A value the
// API does not know is UNSPECIFIED.
func segmentTypeToProto(segmentType SegmentType) managev1.SegmentType {
	switch segmentType {
	case SegmentAllMembers:
		return managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS
	case SegmentMemberTags:
		return managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS
	case SegmentMembersByFilter:
		return managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER
	default:
		return managev1.SegmentType_SEGMENT_TYPE_UNSPECIFIED
	}
}

// segmentTypeFromProto maps an API segment type to the stored value. A value
// the domain does not store keeps its API name, so validation names it in
// "unknown segment type" and a create recognizes segmentTypeUnspecified.
func segmentTypeFromProto(segmentType managev1.SegmentType) SegmentType {
	switch segmentType {
	case managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS:
		return SegmentAllMembers
	case managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS:
		return SegmentMemberTags
	case managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER:
		return SegmentMembersByFilter
	default:
		return SegmentType(segmentType.String())
	}
}
