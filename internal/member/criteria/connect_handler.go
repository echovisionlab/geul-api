package criteria

import (
	"context"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the Criteria Connect RPCs. Each method translates the
// request into a use-case command or query, and the result into the
// response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	criteriaSet *Service
}

// NewConnectHandler returns the Connect adapter of Member criteria.
func NewConnectHandler(criteriaSet *Service) *ConnectHandler {
	return &ConnectHandler{criteriaSet: criteriaSet}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func respondCriteria(view CriteriaView, err error) (*connect.Response[managev1.MemberCriteria], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(criteriaMessage(view)), nil
}

// criteriaConfigInput translates a requested filter. An API role the domain
// does not know becomes an empty role ID and a malformed timestamp is named,
// so validation rejects both in its own order.
func criteriaConfigInput(config *managev1.MemberCriteriaConfig) *CriteriaConfigInput {
	if config == nil {
		return nil
	}
	input := &CriteriaConfigInput{
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

func (h *ConnectHandler) CreateMemberCriteria(ctx context.Context, req *connect.Request[managev1.CreateMemberCriteriaRequest]) (*connect.Response[managev1.MemberCriteria], error) {
	return respondCriteria(h.criteriaSet.CreateCriteria(ctx, CreateCriteriaCommand{
		Name: req.Msg.Name, Description: req.Msg.Description,
		Kind: kindFromProto(req.Msg.Kind), Config: criteriaConfigInput(req.Msg.Config),
	}))
}

func (h *ConnectHandler) CountMatchingMembers(ctx context.Context, req *connect.Request[managev1.CountMatchingMembersRequest]) (*connect.Response[managev1.CountMatchingMembersResponse], error) {
	count, err := h.criteriaSet.EstimateCriteriaCount(ctx, EstimateCriteriaCountQuery{
		Kind: kindFromProto(req.Msg.Kind), Config: criteriaConfigInput(req.Msg.Config),
	})
	return respond(&managev1.CountMatchingMembersResponse{Count: count}, err)
}

func (h *ConnectHandler) UpdateMemberCriteria(ctx context.Context, req *connect.Request[managev1.UpdateMemberCriteriaRequest]) (*connect.Response[managev1.MemberCriteria], error) {
	command := UpdateCriteriaCommand{
		ID: req.Msg.Id, Name: req.Msg.Name, Description: req.Msg.Description, Config: criteriaConfigInput(req.Msg.Config),
	}
	if req.Msg.Kind != nil {
		kind := kindFromProto(*req.Msg.Kind)
		command.Kind = &kind
	}
	if observed := req.Msg.Observed; observed != nil {
		command.Observed = &ObservedCriteria{
			Kind: kindFromProto(observed.Kind), Config: criteriaConfigInput(observed.Config),
		}
	}
	return respondCriteria(h.criteriaSet.UpdateCriteria(ctx, command))
}

func (h *ConnectHandler) ArchiveMemberCriteria(ctx context.Context, req *connect.Request[managev1.ArchiveMemberCriteriaRequest]) (*connect.Response[managev1.MemberCriteria], error) {
	return respondCriteria(h.criteriaSet.ArchiveCriteria(ctx, ArchiveCriteriaCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) RestoreMemberCriteria(ctx context.Context, req *connect.Request[managev1.RestoreMemberCriteriaRequest]) (*connect.Response[managev1.MemberCriteria], error) {
	return respondCriteria(h.criteriaSet.RestoreCriteria(ctx, RestoreCriteriaCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) GetMemberCriteria(ctx context.Context, req *connect.Request[managev1.GetMemberCriteriaRequest]) (*connect.Response[managev1.MemberCriteria], error) {
	return respondCriteria(h.criteriaSet.GetCriteria(ctx, GetCriteriaQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListMemberCriteriaAdmin(ctx context.Context, req *connect.Request[managev1.ListMemberCriteriaAdminRequest]) (*connect.Response[managev1.ListMemberCriteriaAdminResponse], error) {
	page, err := h.criteriaSet.ListCriteriaSet(ctx, ListCriteriaSetQuery{
		IncludeArchived: req.Msg.IncludeArchived,
		Limit:           req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	criteriaSet := make([]*managev1.MemberCriteria, len(page.CriteriaSet))
	for i, view := range page.CriteriaSet {
		criteriaSet[i] = criteriaMessage(view)
	}
	return connect.NewResponse(&managev1.ListMemberCriteriaAdminResponse{
		MemberCriteria: criteriaSet,
		Pagination:     (queryutil.Pagination{Limit: page.Limit, Offset: page.Offset}).BuildResponse(page.Total),
	}), nil
}

func (h *ConnectHandler) ListMemberCriteriaForAuthenticatedAccess(ctx context.Context, req *connect.Request[managev1.ListMemberCriteriaForAuthenticatedAccessRequest]) (*connect.Response[managev1.ListMemberCriteriaForAuthenticatedAccessResponse], error) {
	page, err := h.criteriaSet.ListAuthenticatedAccessCriteriaSet(ctx, ListAuthenticatedAccessCriteriaSetQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	summaries := make([]*managev1.MemberCriteriaSummary, len(page.CriteriaSet))
	for i, criteria := range page.CriteriaSet {
		summaries[i] = criteriaSummary(criteria)
	}
	return connect.NewResponse(&managev1.ListMemberCriteriaForAuthenticatedAccessResponse{
		MemberCriteria: summaries,
		Pagination:     (queryutil.Pagination{Limit: page.Limit, Offset: page.Offset}).BuildResponse(page.Total),
	}), nil
}

func criteriaMessage(view CriteriaView) *managev1.MemberCriteria {
	criteria := view.Criteria
	message := &managev1.MemberCriteria{
		Id:                           criteria.ID,
		Name:                         criteria.Name,
		Description:                  criteria.Description,
		Kind:                         kindToProto(criteria.Type),
		Config:                       criteriaConfigMessage(criteria.Config),
		CreatedAt:                    timestamppb.New(criteria.CreatedAt),
		CampaignCount:                view.References.Campaigns,
		DeliveryRunCount:             view.References.DeliveryRuns,
		DownloadPolicyReferenceCount: view.References.DownloadPolicies,
		MatchingMemberCount:          view.EstimatedCount,
	}
	if criteria.UpdatedAt != nil {
		message.UpdatedAt = timestamppb.New(*criteria.UpdatedAt)
	}
	if criteria.ArchivedAt != nil {
		message.ArchivedAt = timestamppb.New(*criteria.ArchivedAt)
	}
	return message
}

func criteriaConfigMessage(config Config) *managev1.MemberCriteriaConfig {
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
	return &managev1.MemberCriteriaConfig{
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

// AuthenticatedAccessCriteriaSummary returns the summary restricted download
// policy APIs expose of a criteria, when it can grant authenticated access.
func AuthenticatedAccessCriteriaSummary(criteria Criteria) (*managev1.MemberCriteriaSummary, bool) {
	if !criteria.GrantsAuthenticatedAccess() {
		return nil, false
	}
	return criteriaSummary(criteria), true
}

func criteriaSummary(criteria Criteria) *managev1.MemberCriteriaSummary {
	return &managev1.MemberCriteriaSummary{
		Id:          criteria.ID,
		Name:        criteria.Name,
		Description: criteria.Description,
		Kind:        kindToProto(criteria.Type.trimmed()),
	}
}

// kindToProto maps a stored criteria type to the API enum. A value the
// API does not know is UNSPECIFIED.
func kindToProto(kind Kind) managev1.MemberCriteriaKind {
	switch kind {
	case KindAllMembers:
		return managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_ALL_MEMBERS
	case KindMemberTags:
		return managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_MEMBER_TAGS
	case KindFilter:
		return managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_FILTER
	default:
		return managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_UNSPECIFIED
	}
}

// kindFromProto maps an API criteria type to the stored value. A value
// the domain does not store keeps its API name, so validation names it in
// "unknown criteria type" and a create recognizes kindUnspecified.
func kindFromProto(kind managev1.MemberCriteriaKind) Kind {
	switch kind {
	case managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_ALL_MEMBERS:
		return KindAllMembers
	case managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_MEMBER_TAGS:
		return KindMemberTags
	case managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_FILTER:
		return KindFilter
	default:
		return Kind(kind.String())
	}
}
