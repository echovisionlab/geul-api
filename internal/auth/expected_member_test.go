package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckExpectedMemberBindsWritesToTheSessionMember(t *testing.T) {
	session := WithUser(context.Background(), &UserInfo{
		IdentityID:    IdentityID(testIdentityID),
		MemberID:      MemberID(testMemberID),
		Authenticated: true,
		Onboarded:     true,
	})

	require.NoError(t, CheckExpectedMember(session, ""))
	require.NoError(t, CheckExpectedMember(session, testMemberID))
	require.ErrorIs(t, CheckExpectedMember(session, "22222222-2222-4222-8222-222222222222"), ErrExpectedMemberMismatch)
	require.ErrorIs(t, CheckExpectedMember(context.Background(), testMemberID), ErrExpectedMemberMismatch)
	unauthenticated := WithUser(context.Background(), &UserInfo{MemberID: MemberID(testMemberID)})
	require.ErrorIs(t, CheckExpectedMember(unauthenticated, testMemberID), ErrExpectedMemberMismatch)
}
