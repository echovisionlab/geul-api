package auth

import (
	"testing"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationDecisionPreservesAttributionWithoutChangingEngineKey(t *testing.T) {
	can, err := policyv1.Form.Edit("form-1")
	require.NoError(t, err)
	directContext := WithUser(t.Context(), &UserInfo{
		IdentityID:    IdentityID(typedTestAccountIdentityID),
		MemberID:      MemberID("00000000-0000-4000-8000-000000000002"),
		SessionID:     SessionID("session-1"),
		Authenticated: true,
	})
	direct, err := AuthorizationDecision(directContext, can)
	require.NoError(t, err)
	require.Equal(t, policyv1.DelegationDirectSession, direct.Delegation().Kind())

	mcpDelegation, err := policyv1.MCPOAuth("client-1", "Example Member · Example Client")
	require.NoError(t, err)
	mcpContext := WithUser(t.Context(), &UserInfo{
		IdentityID:    IdentityID(typedTestAccountIdentityID),
		MemberID:      MemberID("00000000-0000-4000-8000-000000000002"),
		Authenticated: true,
	})
	mcpContext, err = WithAuthorizationDelegation(mcpContext, mcpDelegation)
	require.NoError(t, err)
	mcp, err := AuthorizationDecision(mcpContext, can)
	require.NoError(t, err)
	require.Equal(t, policyv1.DelegationMCPOAuth, mcp.Delegation().Kind())
	require.Equal(t, direct.EngineKey(), mcp.EngineKey())
}

func TestAuthorizationDecisionForActorUsesExplicitActorAndRequestDelegation(t *testing.T) {
	can, err := policyv1.Artist.Manage("artist-1")
	require.NoError(t, err)
	requestContext := WithUser(t.Context(), &UserInfo{
		IdentityID:    IdentityID(typedTestAccountIdentityID),
		MemberID:      MemberID("00000000-0000-4000-8000-000000000002"),
		SessionID:     SessionID("session-1"),
		Authenticated: true,
	})
	explicitActor, err := policyv1.NewAccountIdentityActor("00000000-0000-4000-8000-000000000003")
	require.NoError(t, err)

	decision, err := AuthorizationDecisionForActor(requestContext, explicitActor, can)
	require.NoError(t, err)
	require.Equal(t, explicitActor.AccountIdentityID(), decision.Actor().AccountIdentityID())
	require.Equal(t, policyv1.DelegationDirectSession, decision.Delegation().Kind())
	require.Equal(t, "artist\x00artist-1\x00manage", decision.EngineKey())
}

func TestAuthorizationDecisionFailsClosedWithoutValidCanActorOrDelegation(t *testing.T) {
	validCan, err := policyv1.Form.Edit("form-1")
	require.NoError(t, err)
	missingDelegation := WithUser(t.Context(), &UserInfo{
		IdentityID:    IdentityID(typedTestAccountIdentityID),
		MemberID:      MemberID("00000000-0000-4000-8000-000000000002"),
		Authenticated: true,
	})

	_, err = AuthorizationDecision(missingDelegation, validCan)
	require.ErrorContains(t, err, "authorization delegation is missing")
	_, err = AuthorizationDecision(t.Context(), validCan)
	require.ErrorContains(t, err, "authenticated authorization principal is required")
	_, err = AuthorizationDecision(missingDelegation, policyv1.Can{})
	require.ErrorContains(t, err, "Can descriptor")
	_, err = WithAuthorizationDelegation(t.Context(), policyv1.Delegation{})
	require.ErrorContains(t, err, "authorization delegation is invalid")
}
