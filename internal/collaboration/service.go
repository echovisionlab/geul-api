package collaboration

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"gorm.io/gorm"
)

type MemberLoader interface {
	LoadActiveSummary(context.Context, string) (*commonv1.MemberSummary, bool, error)
}

type Service struct {
	intrav1connect.UnimplementedInternalCollaborationAuthorizationServiceHandler
	db       *gorm.DB
	registry *Registry
	members  MemberLoader
}

func NewService(db *gorm.DB, registry *Registry, members MemberLoader) *Service {
	if db == nil || registry == nil || members == nil {
		panic("collaboration authorization requires database, registry, and Member loader")
	}
	return &Service{db: db, registry: registry, members: members}
}

func authorizationPermission(value intrav1.CollaborationPermission) (intrav1.CollaborationPermission, error) {
	switch value {
	case intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW:
		return value, nil
	case intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT:
		return value, nil
	default:
		return intrav1.CollaborationPermission_COLLABORATION_PERMISSION_UNSPECIFIED, errs.InvalidArgument("permission", "must be view or edit")
	}
}

func (s *Service) AuthorizeCollaboration(
	ctx context.Context,
	req *connect.Request[intrav1.AuthorizeCollaborationRequest],
) (*connect.Response[intrav1.AuthorizeCollaborationResponse], error) {
	if req.Msg.Principal == nil || req.Msg.Resource == nil {
		return nil, errs.InvalidArgument("request", "principal and resource are required")
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(
		ctx,
		s.db,
		req.Msg.Principal.GetSessionId(),
	)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) ||
		(principal != nil && (principal.Banned || !principal.Onboarded)) {
		return authorizationDenied(
			intrav1.CollaborationAuthorizationDenialReason_COLLABORATION_AUTHORIZATION_DENIAL_REASON_SESSION_INVALID,
		), nil
	}
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("resolve collaboration principal: %w", err))
	}
	permission, err := authorizationPermission(req.Msg.Permission)
	if err != nil {
		return nil, err
	}
	locale := localization.NormalizeExactSupportedLocale(req.Msg.Resource.GetLocale())
	if locale == nil {
		return nil, errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return authorizationDenied(
			intrav1.CollaborationAuthorizationDenialReason_COLLABORATION_AUTHORIZATION_DENIAL_REASON_SESSION_INVALID,
		), nil
	}
	allowed, err := s.registry.Authorize(
		ctx,
		req.Msg.Resource.GetType(),
		req.Msg.Resource.GetId(),
		permission,
		subject,
	)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			return nil, err
		}
		return nil, errs.Internal(fmt.Errorf("check collaboration permission: %w", err))
	}
	if !allowed {
		return authorizationDenied(
			intrav1.CollaborationAuthorizationDenialReason_COLLABORATION_AUTHORIZATION_DENIAL_REASON_PERMISSION_DENIED,
		), nil
	}

	summary, active, err := s.members.LoadActiveSummary(ctx, principal.MemberID.String())
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !active {
		return authorizationDenied(
			intrav1.CollaborationAuthorizationDenialReason_COLLABORATION_AUTHORIZATION_DENIAL_REASON_SESSION_INVALID,
		), nil
	}
	return connect.NewResponse(&intrav1.AuthorizeCollaborationResponse{
		Authorized: true,
		Member:     summary,
		Locale:     *locale,
	}), nil
}

func authorizationDenied(
	reason intrav1.CollaborationAuthorizationDenialReason,
) *connect.Response[intrav1.AuthorizeCollaborationResponse] {
	return connect.NewResponse(&intrav1.AuthorizeCollaborationResponse{
		Authorized:   false,
		DenialReason: reason,
	})
}
