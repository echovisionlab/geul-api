// Package authz provides common authorization utilities.
package authz

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AuthorizationDecisionChecker is the narrow typed PEP dependency used by
// domain lifecycle fences and test doubles.
type AuthorizationDecisionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

// RequireAuthenticatedPrincipal rejects requests without a trusted
// authenticated principal. It performs no external lookup or authorization
// decision and is safe as a preflight before a transactional Can check.
func RequireAuthenticatedPrincipal(ctx context.Context) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return errs.NotAuthenticated()
	}
	return nil
}

// RequireCan checks one generated business action for the authenticated
// account_identity. Callers must supply the domain-owned Can descriptor rather
// than reconstructing resource or permission keys.
func RequireCan(
	ctx context.Context,
	client AuthorizationDecisionChecker,
	can policyv1.Can,
) error {
	if err := RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}
	if !can.Valid() {
		return errs.InternalMsg("authorization Can descriptor is invalid")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := client.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), can.Resource().Type())
	}
	return nil
}

// RequirePlatformPermission is the platform-scoped specialization retained for
// callers whose generated Can targets platform:global.
func RequirePlatformPermission(ctx context.Context, client AuthorizationDecisionChecker, can policyv1.Can) error {
	if can.Valid() && (can.Resource().Type() != policyv1.Platform.Resource().Type() || can.Resource().ID() != policyv1.Platform.Resource().ID()) {
		return errs.InternalMsg("platform authorization requires a platform resource Can")
	}
	return RequireCan(ctx, client, can)
}

// RequireAdminCan preserves the AdminRequired failure contract while checking
// one domain-owned generated Can descriptor.
func RequireAdminCan(ctx context.Context, client AuthorizationDecisionChecker, can policyv1.Can) error {
	err := RequireCan(ctx, client, can)
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return errs.AdminRequired()
		}
		return err
	}
	return nil
}
