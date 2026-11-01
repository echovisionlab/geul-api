package authentication

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GatewayAccessQuery is one Oathkeeper authorization question: may the
// session of the account Identity pass the gateway with role.
type GatewayAccessQuery struct {
	AccountIdentityID string
	SessionID         string
	Role              policyv1.AuthorizationRole
}

// AuthorizeGatewayAccess is the private coarse authorization boundary called
// by Oathkeeper. It accepts only the generated account/session/role
// contract; object-level authorization remains in API handlers.
func (s *Service) AuthorizeGatewayAccess(ctx context.Context, query GatewayAccessQuery) error {
	identityID, err := uuidutil.ParseCanonical(query.AccountIdentityID, "account_identity_id")
	if err != nil {
		return domainerr.InvalidSession().WithCause(err)
	}
	sessionID, err := uuidutil.ParseCanonical(query.SessionID, "session_id")
	if err != nil {
		return domainerr.InvalidSession().WithCause(err)
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.db, sessionID.String())
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return domainerr.InvalidSession().WithCause(err)
	}
	if err != nil {
		return domainerr.DependencyUnavailable("principal database").WithCause(err)
	}
	if principal.IdentityID != auth.IdentityID(identityID.String()) || principal.Banned || !principal.Onboarded {
		return domainerr.InvalidSession()
	}
	can, ok, err := gatewayAuthorizationCan(query.Role)
	if err != nil {
		return err
	}
	if !ok {
		return domainerr.InvalidField("role", "unsupported gateway authorization role")
	}
	authorizationCtx := auth.WithUser(ctx, principal)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return domainerr.InvalidSession().WithCause(err)
	}
	authorized, err := s.spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !authorized {
		return domainerr.Denied(fmt.Sprintf("%s access required", query.Role.String()))
	}
	return nil
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
