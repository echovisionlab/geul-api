package account_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// timedBan bans identityID in Kratos and in the store until until.
func (f fixture) timedBan(identityID string, until time.Time) {
	f.kratos.Update(identityID, func(identity *auth.Identity) {
		identity.State = auth.KratosStateInactive
		identity.MetadataAdmin = structured.Fields{"banned": true, "ban_expires": until.Format(time.RFC3339)}
	})
	f.store.UpdateIdentity(identityID, func(identity *accounttest.Identity) {
		identity.State, identity.Banned = auth.KratosStateInactive, true
	})
}

func TestClearExpiredTimedBanLiftsTheBanAndRecordsTheBackend(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	now := time.Now().UTC()
	f.timedBan(identityID, now.Add(-time.Minute))

	cleared, err := account.ClearExpiredTimedBan(t.Context(), f.units, f.kratos, identityID, now, f.audit)

	require.NoError(t, err)
	require.True(t, cleared)
	identity := f.kratos.Identity(identityID)
	require.False(t, identity.IsBanned())
	require.Equal(t, auth.KratosStateActive, identity.State)
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditMemberUpdated, records[0].Action)
	require.Equal(t, memberID, records[0].TargetID)
	require.Equal(t, sharedtelemetry.AuditStateBanned, records[0].PreviousState)
	require.Equal(t, sharedtelemetry.AuditStateActive, records[0].NewState)
	require.Equal(t, sharedtelemetry.ActorKindSystem, records[0].Kind)
	require.Equal(t, string(sharedtelemetry.ServiceBackend), records[0].Service)
	// The pending-deletion read, the Member read and the audit are three
	// steps under one fence; the Kratos writes commit between them.
	require.Equal(t, []string{identityID}, f.units.FencedIdentities())
	require.Equal(t, []int{3}, f.units.FencedSteps())
}

func TestClearExpiredTimedBanKeepsAnUnexpiredBanOrAPendingDeletion(t *testing.T) {
	t.Parallel()
	t.Run("unexpired", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		_, identityID := f.seedPair("")
		now := time.Now().UTC()
		f.timedBan(identityID, now.Add(time.Hour))

		cleared, err := account.ClearExpiredTimedBan(t.Context(), f.units, f.kratos, identityID, now, f.audit)

		require.NoError(t, err)
		require.False(t, cleared)
		require.True(t, f.kratos.Identity(identityID).IsBanned())
		require.Empty(t, f.kratos.Writes())
		require.Empty(t, f.audit.Records())
	})
	t.Run("pending deletion", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		memberID, identityID := f.seedPair("")
		now := time.Now().UTC()
		f.timedBan(identityID, now.Add(-time.Minute))
		f.store.SeedDeletionRequest(account.DeletionRequest{
			ID: uuid.NewString(), MemberID: memberID, IdentityID: identityID, Token: uuid.NewString(),
			TokenExpiresAt: now.Add(time.Hour), LifecycleState: "scheduled", ScheduledAt: &now, CreatedAt: now, UpdatedAt: now,
		})

		cleared, err := account.ClearExpiredTimedBan(t.Context(), f.units, f.kratos, identityID, now, f.audit)

		require.NoError(t, err)
		require.False(t, cleared)
		require.Empty(t, f.kratos.Calls())
		require.Empty(t, f.audit.Records())
	})
}

// A deletion deadline deactivates the Identity and revokes its sessions;
// a cancelled one reactivates it. Each repair runs under the Identity fence.
func TestLifecycleReconcilersRepairIdentityDrift(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	scheduledMemberID, scheduledIdentityID := f.seedPair("")
	cancelledMemberID, cancelledIdentityID := f.seedPair("")
	now := time.Now().UTC()
	f.store.SeedDeletionRequest(account.DeletionRequest{
		ID: uuid.NewString(), MemberID: scheduledMemberID, IdentityID: scheduledIdentityID, Token: uuid.NewString(),
		TokenExpiresAt: now.Add(time.Hour), LifecycleState: "scheduled", ScheduledAt: &now, CreatedAt: now, UpdatedAt: now,
	})
	f.store.SeedDeletionRequest(account.DeletionRequest{
		ID: uuid.NewString(), MemberID: cancelledMemberID, IdentityID: cancelledIdentityID, Token: uuid.NewString(),
		TokenExpiresAt: now.Add(time.Hour), LifecycleState: "cancelled", CreatedAt: now, UpdatedAt: now,
	})
	f.kratos.Update(cancelledIdentityID, func(identity *auth.Identity) { identity.State = auth.KratosStateInactive })
	f.store.UpdateIdentity(cancelledIdentityID, func(identity *accounttest.Identity) { identity.State = auth.KratosStateInactive })

	require.NoError(t, f.service.ReconcileScheduledIdentityStates(t.Context()))
	require.NoError(t, f.service.ReconcileTerminalIdentityStates(t.Context()))

	require.Equal(t, auth.KratosStateInactive, f.kratos.Identity(scheduledIdentityID).State)
	require.Equal(t, auth.KratosStateActive, f.kratos.Identity(cancelledIdentityID).State)
	require.Equal(t, []string{
		"SetIdentityState:" + scheduledIdentityID, "DeleteIdentitySessions:" + scheduledIdentityID,
		"SetIdentityState:" + cancelledIdentityID,
	}, f.kratos.Writes())
	require.Equal(t, []string{scheduledIdentityID, cancelledIdentityID}, f.units.FencedIdentities())

	// Nothing drifts any more.
	require.NoError(t, f.service.ReconcileScheduledIdentityStates(t.Context()))
	require.NoError(t, f.service.ReconcileTerminalIdentityStates(t.Context()))
	require.Len(t, f.kratos.Writes(), 3)
}
