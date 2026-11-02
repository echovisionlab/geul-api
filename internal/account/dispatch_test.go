package account_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// dueRequest stores a scheduled deletion of memberID's pair that fell due a
// minute ago, with its notification snapshot.
func (f fixture) dueRequest(memberID, identityID string) account.DeletionRequest {
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	email, name, locale := identityID+"@example.test", "Scheduled member", "ko"
	request := account.DeletionRequest{
		ID: uuid.NewString(), MemberID: memberID, IdentityID: identityID, Token: uuid.NewString(),
		TokenExpiresAt: due, LifecycleState: "scheduled", ScheduledAt: &due,
		NotificationEmail: &email, NotificationEmailVerifiedAt: &now, NotificationName: &name, NotificationLocale: &locale,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	f.store.SeedDeletionRequest(request)
	return request
}

// A due request of an inactive Identity becomes the identity and avatar
// commands and is removed, in one transaction under the Identity fence.
func TestDispatchScheduledUserDeletionEnqueuesBothCommandsAndRemovesTheRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	f.store.UpdateIdentity(identityID, func(identity *accounttest.Identity) { identity.State = auth.KratosStateInactive })
	f.store.UpdateMember(memberID, func(member *accounttest.Member) { member.AvatarAssetID = "avatar-asset" })
	request := f.dueRequest(memberID, identityID)

	require.NoError(t, account.DispatchScheduledUserDeletion(t.Context(), f.units, f.authorizer, request.ID, time.Now()))

	commands := f.store.IdentityCommands()
	require.Len(t, commands, 1)
	require.Equal(t, account.DeleteIdentityTombstone, commands[0].Mode)
	require.Equal(t, memberID, commands[0].MemberID)
	require.Equal(t, *request.NotificationEmail, *commands[0].NotificationEmail)
	require.Equal(t, *request.NotificationName, *commands[0].NotificationName)
	require.Equal(t, "ko", *commands[0].NotificationLocale)
	require.Equal(t, "avatar-asset", *commands[0].AvatarAssetID)
	avatars := f.store.AvatarCommands()
	require.Len(t, avatars, 1)
	require.Equal(t, memberID, avatars[0].MemberID)
	require.Equal(t, "avatar-asset", *avatars[0].AvatarAssetID)
	require.Empty(t, f.store.DeletionRequests())
	require.Equal(t, []string{identityID}, f.store.LockedIdentities())
}

func TestDispatchScheduledUserDeletionLeavesAnActiveIdentityOrTheLastAdmin(t *testing.T) {
	t.Parallel()
	t.Run("active identity", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		memberID, identityID := f.seedPair("")
		request := f.dueRequest(memberID, identityID)

		err := account.DispatchScheduledUserDeletion(t.Context(), f.units, f.authorizer, request.ID, time.Now())

		require.ErrorIs(t, err, account.ErrScheduledDeletionIdentityActive)
		require.Empty(t, f.store.IdentityCommands())
		require.Len(t, f.store.DeletionRequests(), 1)
	})
	t.Run("last admin", func(t *testing.T) {
		t.Parallel()
		checker := &authztest.Checker{}
		f := newFixture(t, checker)
		memberID, identityID := f.seedPair("")
		grantRole(f, checker, identityID, policyv1.Role.Admin())
		f.store.UpdateIdentity(identityID, func(identity *accounttest.Identity) { identity.State = auth.KratosStateInactive })
		request := f.dueRequest(memberID, identityID)

		err := account.DispatchScheduledUserDeletion(t.Context(), f.units, f.authorizer, request.ID, time.Now())

		require.ErrorIs(t, err, account.ErrLastActiveAdminDeletion)
		require.Empty(t, f.store.IdentityCommands())
		require.Empty(t, f.store.AvatarCommands())
		require.Len(t, f.store.DeletionRequests(), 1)
	})
	t.Run("not due", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		memberID, identityID := f.seedPair("")
		request := f.dueRequest(memberID, identityID)

		require.NoError(t, account.DispatchScheduledUserDeletion(t.Context(), f.units, f.authorizer, request.ID, time.Now().Add(-time.Hour)))

		require.Empty(t, f.store.IdentityCommands())
		require.Len(t, f.store.DeletionRequests(), 1)
	})
}

func TestAccountSummariesReadStatusAndPendingDeletion(t *testing.T) {
	t.Parallel()
	checker := &authztest.Checker{}
	f := newFixture(t, checker)
	activeID, activeIdentityID := f.seedPair("")
	bannedID, bannedIdentityID := f.seedPair("")
	pendingID, pendingIdentityID := f.seedPair("")
	for _, identityID := range []string{activeIdentityID, bannedIdentityID, pendingIdentityID} {
		grantRole(f, checker, identityID, policyv1.Role.User())
	}
	f.store.UpdateIdentity(bannedIdentityID, func(identity *accounttest.Identity) {
		identity.State, identity.Banned = auth.KratosStateInactive, true
	})
	f.store.UpdateIdentity(pendingIdentityID, func(identity *accounttest.Identity) { identity.State = auth.KratosStateInactive })
	f.dueRequest(pendingID, pendingIdentityID)

	summaries, err := f.service.Summaries(t.Context(), []string{activeID, bannedID, pendingID})

	require.NoError(t, err)
	require.Equal(t, account.StatusActive, summaries[activeID].Status)
	require.Equal(t, account.StatusBanned, summaries[bannedID].Status)
	require.Equal(t, account.StatusPendingDeletion, summaries[pendingID].Status)
}
