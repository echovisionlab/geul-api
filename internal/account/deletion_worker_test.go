package account_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (f fixture) deletion() account.UserDeletion {
	return account.UserDeletion{Units: f.units, Identities: f.kratos, SpiceDB: f.authorizer, Members: f.members, Emails: f.emails, Audit: f.audit}
}

// unonboardedTarget makes memberID an unonboarded Member created at.
func (f fixture) unonboarded(memberID string, created time.Time) {
	f.store.UpdateMember(memberID, func(member *accounttest.Member) { member.Onboarded, member.CreatedAt = false, created })
}

func (f fixture) terminalAudits(memberID string) []sharedtelemetry.AuditRecord {
	var records []sharedtelemetry.AuditRecord
	for _, record := range f.audit.Records() {
		if record.Action == sharedtelemetry.AuditAccountDeleted && record.TargetID == memberID {
			records = append(records, record)
		}
	}
	return records
}

func TestAdminUnonboardedDeleteQueuesHardDeleteWithActorAudit(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())
	a.unonboarded(a.memberID, time.Now().UTC())

	response, err := a.handler.DeleteAccount(a.ctx(t), connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: a.memberID}))

	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	require.NotContains(t, a.kratos.Writes(), "DeleteIdentity:"+a.identityID, "the request must not delete the Kratos identity synchronously")
	commands := a.store.IdentityCommands()
	require.Len(t, commands, 1)
	require.Equal(t, account.DeleteIdentityUnonboardedHardDelete, commands[0].Mode)
	require.Equal(t, a.memberID, commands[0].MemberID)
	require.Equal(t, a.identityID, commands[0].IdentityID)
	records := a.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditAccountUpdated, records[0].Action)
	require.Equal(t, "account", records[0].TargetType)
	require.Equal(t, sharedtelemetry.ActorKindMember, records[0].Kind)
	require.Equal(t, a.actorID, records[0].MemberID)
	require.Equal(t, sharedtelemetry.AuditStateNone, records[0].PreviousState)
	require.Equal(t, sharedtelemetry.AuditStateScheduled, records[0].NewState)
}

func TestUnonboardedDeleteAuditFailureRollsBackQueueInsert(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())
	a.unonboarded(a.memberID, time.Now().UTC())
	a.audit.Err = errInjected

	_, err := a.handler.DeleteAccount(a.ctx(t), connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: a.memberID}))

	require.ErrorIs(t, err, errInjected)
	require.Empty(t, a.store.IdentityCommands())
}

func TestEnqueueExpiredUnonboardedMembersCommitsOnlyDurableCommands(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	now := time.Now().UTC()
	targetID, targetIdentityID := f.seedPair("")
	recentID, _ := f.seedPair("")
	onboardedID, _ := f.seedPair("")
	f.unonboarded(targetID, now.Add(-account.UnonboardedMemberRetention-time.Hour))
	f.unonboarded(recentID, now.Add(-account.UnonboardedMemberRetention+time.Hour))
	f.store.UpdateMember(onboardedID, func(member *accounttest.Member) {
		member.CreatedAt = now.Add(-account.UnonboardedMemberRetention - time.Hour)
	})

	queued, err := account.EnqueueExpiredUnonboardedMembers(t.Context(), f.units, now, account.UnonboardedMemberCleanupBatchSize)

	require.NoError(t, err)
	require.Equal(t, 1, queued)
	require.Equal(t, []account.DeleteIdentityCommand{{
		Mode: account.DeleteIdentityUnonboardedHardDelete, MemberID: targetID, IdentityID: targetIdentityID,
	}}, f.store.IdentityCommands())
	require.Equal(t, []string{targetIdentityID}, f.store.LockedIdentities())
	_, found := f.store.Member(targetID)
	require.True(t, found)
	require.NotNil(t, f.kratos.Identity(targetIdentityID))
	require.Empty(t, f.kratos.Writes())
}

func TestEnqueueExpiredUnonboardedMembersRollsBackQueueWrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	now := time.Now().UTC()
	targetID, _ := f.seedPair("")
	f.unonboarded(targetID, now.Add(-account.UnonboardedMemberRetention-time.Hour))
	f.store.Fail("EnqueueUserDeleteIdentity", errInjected)

	queued, err := account.EnqueueExpiredUnonboardedMembers(t.Context(), f.units, now, 1)

	require.Zero(t, queued)
	require.ErrorIs(t, err, errInjected)
	require.Empty(t, f.store.IdentityCommands())
}

// PGMQ is at-least-once: a redelivery after the terminal commit is a no-op,
// with no second terminal audit or completion mail.
func TestUnonboardedDeletionWorkerHardDeletesOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	f.unonboarded(memberID, time.Now().UTC().Add(-account.UnonboardedMemberRetention-time.Hour))
	command := account.DeleteIdentityCommand{Mode: account.DeleteIdentityUnonboardedHardDelete, MemberID: memberID, IdentityID: identityID}

	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), command))
	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), command))

	_, found := f.store.Member(memberID)
	require.False(t, found)
	require.Nil(t, f.kratos.Identity(identityID))
	require.False(t, f.store.Anchor(identityID))
	require.Equal(t, []string{identityID}, f.authorizer.Deleted())
	require.Equal(t, []account.MemberUnonboardedDeletionTarget{{MemberID: memberID, IdentityID: identityID, IdentityLinked: true}}, f.members.Revoked())
	records := f.terminalAudits(memberID)
	require.Len(t, records, 1)
	require.Equal(t, "account", records[0].TargetType)
	require.Equal(t, sharedtelemetry.ActorKindSystem, records[0].Kind)
	require.Equal(t, string(sharedtelemetry.ServiceBackend), records[0].Service)
	require.Empty(t, f.emails.Sent())
	require.Equal(t, []string{identityID, identityID}, f.units.FencedIdentities())
}

func TestUnonboardedDeletionWorkerSkipsStaleOnboardedCommand(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	command := account.DeleteIdentityCommand{Mode: account.DeleteIdentityUnonboardedHardDelete, MemberID: memberID, IdentityID: identityID}

	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), command))

	_, found := f.store.Member(memberID)
	require.True(t, found)
	require.NotNil(t, f.kratos.Identity(identityID))
	require.Empty(t, f.kratos.Writes())
	require.Empty(t, f.terminalAudits(memberID))
	require.Empty(t, f.emails.Sent())
}

// The only tolerated unlinked-member replay: a prior delivery completed the
// Kratos and anchor cleanup and crashed before its final Member step.
func TestUnonboardedDeletionWorkerReplaysAfterAnchorCleanup(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	f.unonboarded(memberID, time.Now().UTC())
	require.NoError(t, f.kratos.DeleteIdentity(t.Context(), identityID))
	require.NoError(t, f.store.DeleteAccountIdentityAnchor(t.Context(), identityID))
	member, _ := f.store.Member(memberID)
	require.Empty(t, member.IdentityID)

	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), account.DeleteIdentityCommand{
		Mode: account.DeleteIdentityUnonboardedHardDelete, MemberID: memberID, IdentityID: identityID,
	}))

	_, found := f.store.Member(memberID)
	require.False(t, found)
	require.Len(t, f.terminalAudits(memberID), 1)
	require.Len(t, f.members.Revoked(), 1)
}

// A replay after Kratos deleted the Identity, and before the tombstone
// committed, resumes at the tombstone.
func TestProcessUserDeleteIdentityResumesAfterIdentityDeletionBeforeTombstone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	email, nickname := identityID+"@example.test", "Crash Replay"
	require.NoError(t, f.kratos.DeleteIdentity(t.Context(), identityID))

	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), account.DeleteIdentityCommand{
		Mode: account.DeleteIdentityTombstone, MemberID: memberID, IdentityID: identityID,
		NotificationEmail: &email, NotificationName: &nickname,
	}))

	member, _ := f.store.Member(memberID)
	require.Empty(t, member.IdentityID)
	require.True(t, member.Deleted)
	require.Equal(t, []string{memberID}, f.store.Finalized())
	require.Empty(t, f.store.AvatarCommands())
	require.Len(t, f.emails.Sent(), 1)
	require.Equal(t, email, f.emails.Sent()[0].Recipient)
	require.Equal(t, "account-deletion-complete:"+memberID, f.emails.Sent()[0].MessageID)
}

func TestProcessUserDeleteIdentityTombstonesAndRecordsTheTerminalAuditOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	email, nickname := identityID+"@example.test", "Preserved name"
	command := account.DeleteIdentityCommand{
		Mode: account.DeleteIdentityTombstone, MemberID: memberID, IdentityID: identityID,
		NotificationEmail: &email, NotificationName: &nickname,
	}

	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), command))
	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), command))

	member, _ := f.store.Member(memberID)
	require.True(t, member.Deleted)
	require.Equal(t, email, member.PrimaryEmail)
	require.Nil(t, f.kratos.Identity(identityID))
	require.False(t, f.store.Anchor(identityID))
	require.Len(t, f.terminalAudits(memberID), 1)
	require.Len(t, f.emails.Sent(), 2, "the completion mail is at-least-once across redeliveries")
	require.Equal(t, f.emails.Sent()[0].MessageID, f.emails.Sent()[1].MessageID)
	require.Equal(t, []string{"DeleteIdentity:" + identityID}, f.kratos.Writes())
}

func TestProcessUserDeleteIdentitySkipsCompletionMailWhenNeverOnboarded(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	memberID, identityID := f.seedPair("")
	f.unonboarded(memberID, time.Now().UTC())
	email := identityID + "@example.test"

	require.NoError(t, f.deletion().ProcessIdentity(t.Context(), account.DeleteIdentityCommand{
		Mode: account.DeleteIdentityUnonboardedHardDelete, MemberID: memberID, IdentityID: identityID,
		NotificationEmail: &email, NotificationName: &memberID,
	}))

	require.Empty(t, f.emails.Sent())
}
