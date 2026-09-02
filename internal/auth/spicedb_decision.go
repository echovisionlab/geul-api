package auth

import (
	"context"
	"fmt"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type authorizationDelegationContextKey struct{}

// WithAuthorizationDelegation preserves gateway-validated request attribution
// for downstream domain policy enforcement. Delegation never selects a role,
// resource, action, or SpiceDB permission.
func WithAuthorizationDelegation(
	ctx context.Context,
	delegation policyv1.Delegation,
) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("authorization delegation context is required")
	}
	if !delegation.Valid() {
		return nil, fmt.Errorf("authorization delegation is invalid")
	}
	return context.WithValue(ctx, authorizationDelegationContextKey{}, delegation), nil
}

// AuthorizationDecision combines the authenticated account identity, request
// attribution, and one generated domain Can descriptor. Direct requests derive
// attribution from the validated SessionID; delegated requests must have been
// populated by their trusted gateway adapter.
func AuthorizationDecision(
	ctx context.Context,
	can policyv1.Can,
) (policyv1.AuthorizationDecision, error) {
	if ctx == nil {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization context is required")
	}
	principal := GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authenticated authorization principal is required")
	}
	subject, err := NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization actor: %w", err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization actor: %w", err)
	}
	return AuthorizationDecisionForActor(ctx, actor, can)
}

// AuthorizationDecisionForActor builds a request decision whose explicit
// Actor is the permission subject while the authenticated request supplies
// delegation attribution only. Callers must already have authoritatively
// resolved that subject; internal/system capability checks use CheckActorCan.
func AuthorizationDecisionForActor(
	ctx context.Context,
	actor policyv1.Actor,
	can policyv1.Can,
) (policyv1.AuthorizationDecision, error) {
	if ctx == nil || !actor.Valid() || !can.Valid() {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization context, actor, and Can descriptor are required")
	}
	principal := GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authenticated authorization principal is required")
	}
	if _, err := NewAccountIdentitySubject(IdentityID(actor.AccountIdentityID())); err != nil {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization actor: %w", err)
	}

	delegation, ok := ctx.Value(authorizationDelegationContextKey{}).(policyv1.Delegation)
	if ok {
		if !delegation.Valid() {
			return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization delegation is invalid")
		}
	} else {
		if principal.SessionID == "" {
			return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization delegation is missing")
		}
		var err error
		delegation, err = policyv1.DirectSession(principal.SessionID.String())
		if err != nil {
			return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization delegation: %w", err)
		}
	}

	decision, err := policyv1.NewAuthorizationDecision(actor, delegation, can)
	if err != nil {
		return policyv1.AuthorizationDecision{}, fmt.Errorf("authorization decision: %w", err)
	}
	return decision, nil
}
