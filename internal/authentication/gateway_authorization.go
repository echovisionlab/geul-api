package authentication

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// GatewayAuthorizationService is the private coarse authorization boundary
// called by Oathkeeper. It accepts only the generated account/session/role
// contract; object-level authorization remains in API handlers.
type GatewayAuthorizationService struct {
	intrav1connect.UnimplementedInternalGatewayAuthorizationServiceHandler
	db      *gorm.DB
	spicedb *auth.SpiceDBClient
}

func NewGatewayAuthorizationService(db *gorm.DB, spicedb *auth.SpiceDBClient) *GatewayAuthorizationService {
	if db == nil || spicedb == nil {
		panic("gateway authorization requires database and SpiceDB")
	}
	return &GatewayAuthorizationService{db: db, spicedb: spicedb}
}

func (s *GatewayAuthorizationService) AuthorizeGatewayAccess(
	ctx context.Context,
	req *connect.Request[intrav1.AuthorizeGatewayAccessRequest],
) (*connect.Response[intrav1.AuthorizeGatewayAccessResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	identityID, err := uuidutil.ParseCanonical(req.Msg.GetAccountIdentityId(), "account_identity_id")
	if err != nil {
		return nil, errs.InvalidSession()
	}
	sessionID, err := uuidutil.ParseCanonical(req.Msg.GetSessionId(), "session_id")
	if err != nil {
		return nil, errs.InvalidSession()
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.db, sessionID.String())
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return nil, errs.InvalidSession()
	}
	if err != nil {
		return nil, errs.DependencyUnavailable("principal database")
	}
	if principal.IdentityID != auth.IdentityID(identityID.String()) || principal.Banned || !principal.Onboarded {
		return nil, errs.InvalidSession()
	}
	can, ok, err := gatewayAuthorizationCan(req.Msg.GetRole())
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !ok {
		return nil, errs.InvalidArgument("role", "unsupported gateway authorization role")
	}
	authorizationCtx := auth.WithUser(ctx, principal)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return nil, errs.InvalidSession()
	}
	authorized, err := s.spicedb.Can(authorizationCtx, decision)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if !authorized {
		return nil, errs.PermissionDenied(fmt.Sprintf("%s access required", req.Msg.GetRole().String()))
	}
	return connect.NewResponse(&intrav1.AuthorizeGatewayAccessResponse{}), nil
}

func gatewayAuthorizationCan(role policyv1.AuthorizationRole) (policyv1.Can, bool, error) {
	switch role {
	case policyv1.AuthorizationRole_ADMIN:
		can, err := policyv1.Platform.IsAdmin()
		return can, true, err
	case policyv1.AuthorizationRole_AUTHOR:
		can, err := policyv1.Platform.IsAuthor()
		return can, true, err
	default:
		return policyv1.Can{}, false, nil
	}
}
