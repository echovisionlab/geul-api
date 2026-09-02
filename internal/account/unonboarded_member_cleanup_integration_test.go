//go:build integration

package account

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mq"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type enqueueThenFailUserDeletionPublisher struct {
	delegate UserDeletionIdentityDispatchPublisher
}

func (p enqueueThenFailUserDeletionPublisher) PublishUserDeleteIdentityWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	command *managev1.UserDeleteIdentityCommand,
) error {
	if err := p.delegate.PublishUserDeleteIdentityWithExecutor(ctx, executor, command); err != nil {
		return err
	}
	return errors.New("simulate enqueue transaction rollback")
}

func TestEnqueueExpiredUnonboardedMembersCommitsOnlyDurableCommands(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	recent := stack.CreateUser(t, policyv1.Role.User().ID())
	onboarded := stack.CreateUser(t, policyv1.Role.User().ID())
	now := time.Now().UTC()
	require.NoError(t, stack.DB.Exec(
		"UPDATE member SET onboarded = FALSE, created_at = ?, updated_at = ? WHERE id = ?::uuid",
		now.Add(-UnonboardedMemberRetention-time.Hour), now, target.MemberID,
	).Error)
	require.NoError(t, stack.DB.Exec(
		"UPDATE member SET onboarded = FALSE, created_at = ?, updated_at = ? WHERE id = ?::uuid",
		now.Add(-UnonboardedMemberRetention+time.Hour), now, recent.MemberID,
	).Error)
	require.NoError(t, stack.DB.Exec(
		"UPDATE member SET created_at = ?, updated_at = ? WHERE id = ?::uuid",
		now.Add(-UnonboardedMemberRetention-time.Hour), now, onboarded.MemberID,
	).Error)

	sqlDB, publisher := unonboardedDeletionQueue(t, stack)
	queued, err := EnqueueExpiredUnonboardedMembers(
		t.Context(), stack.DB, publisher, memberDeletionIntegrationAdapter{}, now, UnonboardedMemberCleanupBatchSize,
	)
	require.NoError(t, err)
	require.Equal(t, 1, queued)

	message, command := requireUnonboardedDeletionCommand(t, sqlDB)
	require.Equal(t, target.MemberID, command.GetMemberId())
	require.Equal(t, target.IdentityID, command.GetIdentityId())
	require.Equal(t, managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE, command.GetMode())
	require.NoError(t, testutil.CompletePGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, message.TransportID))

	var identityCount, memberCount int64
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM kratos.identities WHERE id = ?::uuid", target.IdentityID).Scan(&identityCount).Error)
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM member WHERE id = ?::uuid", target.MemberID).Scan(&memberCount).Error)
	require.Equal(t, int64(1), identityCount)
	require.Equal(t, int64(1), memberCount)
}

func TestEnqueueExpiredUnonboardedMembersRollsBackQueueWrite(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	now := time.Now().UTC()
	require.NoError(t, stack.DB.Exec(
		"UPDATE member SET onboarded = FALSE, created_at = ?, updated_at = ? WHERE id = ?::uuid",
		now.Add(-UnonboardedMemberRetention-time.Hour), now, target.MemberID,
	).Error)

	sqlDB, publisher := unonboardedDeletionQueue(t, stack)
	queued, err := EnqueueExpiredUnonboardedMembers(
		t.Context(), stack.DB, enqueueThenFailUserDeletionPublisher{delegate: publisher}, memberDeletionIntegrationAdapter{}, now, 1,
	)
	require.Zero(t, queued)
	require.ErrorContains(t, err, "simulate enqueue transaction rollback")
	messages, err := testutil.ReadPGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, time.Minute, 1)
	require.NoError(t, err)
	require.Empty(t, messages)
}

func TestUnonboardedDeletionWorkerHardDeletesOnceAndCleansSpiceDB(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	now := time.Now().UTC()
	require.NoError(t, stack.DB.Exec(
		"UPDATE member SET onboarded = FALSE, created_at = ?, updated_at = ? WHERE id = ?::uuid",
		now.Add(-UnonboardedMemberRetention-time.Hour), now, target.MemberID,
	).Error)

	seedUnonboardedMemberPolicy(t, stack, target.MemberID)
	requireUnonboardedMemberView(t, stack.SpiceDBClient, target.MemberID, admin.IdentityID, true)

	sqlDB, publisher := unonboardedDeletionQueue(t, stack)
	queued, err := EnqueueExpiredUnonboardedMembers(t.Context(), stack.DB, publisher, memberDeletionIntegrationAdapter{}, now, 1)
	require.NoError(t, err)
	require.Equal(t, 1, queued)
	message, command := requireUnonboardedDeletionCommand(t, sqlDB)

	fanout := &recordingDeletionFanout{}
	writer := apitelemetry.NewDurableWriter(stack.DB)
	require.NoError(t, ProcessUserDeleteIdentityAudited(
		t.Context(), stack.DB, stack.KratosClient, stack.SpiceDBClient, memberDeletionIntegrationAdapter{}, fanout, writer, command,
	))
	// PGMQ is at-least-once. A redelivery after the terminal commit must be a
	// no-op, including no second terminal audit or completion mail.
	require.NoError(t, ProcessUserDeleteIdentityAudited(
		t.Context(), stack.DB, stack.KratosClient, stack.SpiceDBClient, memberDeletionIntegrationAdapter{}, fanout, writer, command,
	))
	require.NoError(t, testutil.CompletePGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, message.TransportID))

	var memberCount, identityCount, anchorCount, terminalAuditCount int64
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM member WHERE id = ?::uuid", target.MemberID).Scan(&memberCount).Error)
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM kratos.identities WHERE id = ?::uuid", target.IdentityID).Scan(&identityCount).Error)
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM account_identity WHERE id = ?::uuid", target.IdentityID).Scan(&anchorCount).Error)
	require.NoError(t, stack.DB.Raw(`
		SELECT COUNT(*) FROM public.domain_audit
		WHERE action = ? AND target_type = 'account' AND target_id = ?
		  AND actor_kind = 'system' AND actor_service = ?
	`, sharedtelemetry.AuditAccountDeleted, target.MemberID, sharedtelemetry.ServiceBackend).Scan(&terminalAuditCount).Error)
	require.Zero(t, memberCount)
	require.Zero(t, identityCount)
	require.Zero(t, anchorCount)
	require.Equal(t, int64(1), terminalAuditCount)
	require.Empty(t, fanout.emails)

	targetSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(target.IdentityID))
	require.NoError(t, err)
	_, hasRole, err := stack.SpiceDBClient.ReadDirectGlobalRole(t.Context(), targetSubject)
	require.NoError(t, err)
	require.False(t, hasRole)
	requireUnonboardedMemberView(t, stack.SpiceDBClient, target.MemberID, admin.IdentityID, false)
}

func TestUnonboardedDeletionWorkerSkipsStaleOnboardedCommand(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	now := time.Now().UTC()
	require.NoError(t, stack.DB.Exec(
		"UPDATE member SET onboarded = FALSE, created_at = ?, updated_at = ? WHERE id = ?::uuid",
		now.Add(-UnonboardedMemberRetention-time.Hour), now, target.MemberID,
	).Error)

	sqlDB, publisher := unonboardedDeletionQueue(t, stack)
	queued, err := EnqueueExpiredUnonboardedMembers(t.Context(), stack.DB, publisher, memberDeletionIntegrationAdapter{}, now, 1)
	require.NoError(t, err)
	require.Equal(t, 1, queued)
	message, command := requireUnonboardedDeletionCommand(t, sqlDB)
	require.NoError(t, stack.DB.Exec("UPDATE member SET onboarded = TRUE WHERE id = ?::uuid", target.MemberID).Error)

	fanout := &recordingDeletionFanout{}
	require.NoError(t, ProcessUserDeleteIdentityAudited(
		t.Context(), stack.DB, stack.KratosClient, stack.SpiceDBClient, memberDeletionIntegrationAdapter{}, fanout, apitelemetry.NewDurableWriter(stack.DB), command,
	))
	require.NoError(t, testutil.CompletePGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, message.TransportID))

	var memberCount, identityCount, terminalAuditCount int64
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM member WHERE id = ?::uuid", target.MemberID).Scan(&memberCount).Error)
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM kratos.identities WHERE id = ?::uuid", target.IdentityID).Scan(&identityCount).Error)
	require.NoError(t, stack.DB.Raw(`
		SELECT COUNT(*) FROM public.domain_audit
		WHERE action = ? AND target_type = 'account' AND target_id = ?
	`, sharedtelemetry.AuditAccountDeleted, target.MemberID).Scan(&terminalAuditCount).Error)
	require.Equal(t, int64(1), memberCount)
	require.Equal(t, int64(1), identityCount)
	require.Zero(t, terminalAuditCount)
	require.Empty(t, fanout.emails)
}

func TestUnonboardedDeletionWorkerReplaysAfterAnchorCleanup(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	require.NoError(t, stack.DB.Exec("UPDATE member SET onboarded = FALSE WHERE id = ?::uuid", target.MemberID).Error)
	seedUnonboardedMemberPolicy(t, stack, target.MemberID)
	requireUnonboardedMemberView(t, stack.SpiceDBClient, target.MemberID, admin.IdentityID, true)

	// This is the only tolerated unlinked-member replay: the command's exact
	// identity is already absent because a prior delivery completed Kratos and
	// anchor cleanup before crashing before its final Member transaction.
	require.NoError(t, stack.KratosClient.DeleteIdentity(t.Context(), target.IdentityID))
	require.NoError(t, stack.DB.Exec("DELETE FROM account_identity WHERE id = ?::uuid", target.IdentityID).Error)
	var link sql.NullString
	require.NoError(t, stack.DB.Raw("SELECT account_identity_id::text FROM member WHERE id = ?::uuid", target.MemberID).Scan(&link).Error)
	require.False(t, link.Valid)

	command := &managev1.UserDeleteIdentityCommand{
		Mode:       managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE,
		MemberId:   target.MemberID,
		IdentityId: target.IdentityID,
	}
	require.NoError(t, ProcessUserDeleteIdentityAudited(
		t.Context(), stack.DB, stack.KratosClient, stack.SpiceDBClient, memberDeletionIntegrationAdapter{}, &recordingDeletionFanout{}, apitelemetry.NewDurableWriter(stack.DB), command,
	))

	var memberCount, terminalAuditCount int64
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM member WHERE id = ?::uuid", target.MemberID).Scan(&memberCount).Error)
	require.NoError(t, stack.DB.Raw(`
		SELECT COUNT(*) FROM public.domain_audit
		WHERE action = ? AND target_type = 'account' AND target_id = ?
		  AND actor_kind = 'system' AND actor_service = ?
	`, sharedtelemetry.AuditAccountDeleted, target.MemberID, sharedtelemetry.ServiceBackend).Scan(&terminalAuditCount).Error)
	require.Zero(t, memberCount)
	require.Equal(t, int64(1), terminalAuditCount)
	requireUnonboardedMemberView(t, stack.SpiceDBClient, target.MemberID, admin.IdentityID, false)
}

func requireUnonboardedMemberView(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	memberID string,
	accountIdentityID string,
	want bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(accountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.Member.View(memberID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed)
}

func unonboardedDeletionQueue(t *testing.T, stack *testutil.OryStack) (*sql.DB, *mq.Publisher) {
	t.Helper()
	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	require.NoError(t, testutil.PurgePGMQQueue(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity))
	t.Cleanup(func() {
		require.NoError(t, testutil.PurgePGMQQueue(context.Background(), sqlDB, eventpkg.QueueUserDeleteIdentity))
	})
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	return sqlDB, publisher
}

func requireUnonboardedDeletionCommand(t *testing.T, sqlDB *sql.DB) (eventpkg.Message, *managev1.UserDeleteIdentityCommand) {
	t.Helper()
	messages, err := testutil.ReadPGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	payload, err := messages[0].Envelope.Payload()
	require.NoError(t, err)
	command := &managev1.UserDeleteIdentityCommand{}
	require.NoError(t, proto.Unmarshal(payload, command))
	return messages[0], command
}

func seedUnonboardedMemberPolicy(t *testing.T, stack *testutil.OryStack, memberID string) {
	t.Helper()
	policy, err := policyv1.Member.TouchPolicy(memberID)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
}
