package audience

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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
	return respond(h.audiences.CreateSegment(ctx, CreateSegmentCommand{
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
	return respond(h.audiences.UpdateSegment(ctx, command))
}

func (h *ConnectHandler) ArchiveSegment(ctx context.Context, req *connect.Request[managev1.ArchiveSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	return respond(h.audiences.ArchiveSegment(ctx, ArchiveSegmentCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) RestoreSegment(ctx context.Context, req *connect.Request[managev1.RestoreSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	return respond(h.audiences.RestoreSegment(ctx, RestoreSegmentCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) GetSegment(ctx context.Context, req *connect.Request[managev1.GetSegmentRequest]) (*connect.Response[managev1.Segment], error) {
	return respond(h.audiences.GetSegment(ctx, GetSegmentQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListSegmentsAdmin(ctx context.Context, req *connect.Request[managev1.ListSegmentsAdminRequest]) (*connect.Response[managev1.ListSegmentsAdminResponse], error) {
	page, err := h.audiences.ListSegments(ctx, ListSegmentsQuery{
		IncludeArchived: req.Msg.IncludeArchived,
		Limit:           req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListSegmentsAdminResponse{Segments: page.Segments, Pagination: page.Pagination}, err)
}

func (h *ConnectHandler) ListSegmentsForAuthenticatedAccess(ctx context.Context, req *connect.Request[managev1.ListSegmentsForAuthenticatedAccessRequest]) (*connect.Response[managev1.ListSegmentsForAuthenticatedAccessResponse], error) {
	page, err := h.audiences.ListAuthenticatedAccessSegments(ctx, ListAuthenticatedAccessSegmentsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respond(&managev1.ListSegmentsForAuthenticatedAccessResponse{Segments: page.Segments, Pagination: page.Pagination}, err)
}
