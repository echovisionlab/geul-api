package emailcontent

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// PermissionChecker reads one authorization decision with full consistency.
type PermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

// RequireEditor authorizes the request's onboarded Member for can, the Edit
// decision of one owner, and returns the Member. canErr rejects an ID that
// cannot name the owner as field; denials name resource.
func RequireEditor(ctx context.Context, checker PermissionChecker, can policyv1.Can, canErr error, field, resource string) (uuid.UUID, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return uuid.Nil, domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return uuid.Nil, domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return uuid.Nil, domainerr.NoPermission("edit", resource)
	}
	memberID, err := uuid.Parse(principal.MemberID.String())
	if err != nil || memberID == uuid.Nil || memberID.String() != principal.MemberID.String() {
		return uuid.Nil, domainerr.AuthenticationRequired()
	}
	if canErr != nil {
		return uuid.Nil, domainerr.InvalidField(field, "must be a canonical UUID")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return uuid.Nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return uuid.Nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return uuid.Nil, domainerr.NoPermission("edit", resource)
	}
	return memberID, nil
}
