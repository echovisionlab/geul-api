package auth

import (
	"context"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestAuthContextKeepsIdentityMemberAndSessionNamespacesDistinct(t *testing.T) {
	principal := &UserInfo{
		IdentityID:    IdentityID(testIdentityID),
		MemberID:      MemberID(testMemberID),
		SessionID:     SessionID(testSessionID),
		Authenticated: true,
	}

	got := GetUser(WithUser(context.Background(), principal))

	require.Same(t, principal, got)
	require.Equal(t, testIdentityID, got.IdentityID.String())
	require.Equal(t, testMemberID, got.MemberID.String())
	require.Equal(t, testSessionID, got.SessionID.String())
	require.NotEqual(t, got.IdentityID.String(), got.MemberID.String())
}

func TestAuthContextExposesOnlyOnboardedMemberAsRequestActor(t *testing.T) {
	requestContext, err := sharedtelemetry.NewPublicRequestContext("127.0.0.1")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(context.Background(), requestContext)

	onboarding := WithUser(ctx, &UserInfo{
		IdentityID:    IdentityID(testIdentityID),
		MemberID:      MemberID(testMemberID),
		Authenticated: true,
		Onboarded:     false,
	})
	resolved, ok := sharedtelemetry.RequestContextFrom(onboarding)
	require.True(t, ok)
	require.IsType(t, sharedtelemetry.AnonymousActor{}, resolved.Actor)

	active := WithUser(ctx, &UserInfo{
		IdentityID:    IdentityID(testIdentityID),
		MemberID:      MemberID(testMemberID),
		Authenticated: true,
		Onboarded:     true,
	})
	resolved, ok = sharedtelemetry.RequestContextFrom(active)
	require.True(t, ok)
	require.Equal(t, sharedtelemetry.MemberActor{
		IdentityID: testIdentityID,
		MemberID:   testMemberID,
	}, resolved.Actor)
}
