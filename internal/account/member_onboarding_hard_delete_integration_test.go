//go:build integration

package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/mq"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type guardedUnonboardedIdentityManager struct {
	accountIdentityManager
	deleteCalled bool
}

func (m *guardedUnonboardedIdentityManager) DeleteIdentity(context.Context, string) error {
	m.deleteCalled = true
	return errors.New("admin request must not delete Kratos identity synchronously")
}

func TestAdminUnonboardedDeleteQueuesHardDeleteWithActorAudit(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	require.NoError(t, stack.DB.Exec("UPDATE member SET onboarded = FALSE WHERE id = ?::uuid", target.MemberID).Error)

	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	require.NoError(t, testutil.PurgePGMQQueue(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity))
	identity := &guardedUnonboardedIdentityManager{accountIdentityManager: stack.KratosClient}
	service := NewAuditedAccountService(
		stack.DB, identity, stack.SpiceDBClient, "https://www.example.test", publisher,
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberDeletion(memberDeletionIntegrationAdapter{}),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	response, err := service.DeleteAccount(
		auditedOryMemberContext(t, admin),
		connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: target.MemberID}),
	)
	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	require.False(t, identity.deleteCalled)

	messages, err := testutil.ReadPGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	payload, err := messages[0].Envelope.Payload()
	require.NoError(t, err)
	var command managev1.UserDeleteIdentityCommand
	require.NoError(t, proto.Unmarshal(payload, &command))
	require.Equal(t, managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE, command.GetMode())
	require.Equal(t, target.MemberID, command.GetMemberId())
	require.NoError(t, testutil.CompletePGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, messages[0].TransportID))

	var audit struct {
		Action        string
		ActorKind     string
		ActorMemberID string
		PreviousState string
		NewState      string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id,
		       attributes->>'previous_state' AS previous_state,
		       attributes->>'new_state' AS new_state
		FROM public.domain_audit
		WHERE target_type = 'account' AND target_id = ?
		  AND action = ?
	`, target.MemberID, sharedtelemetry.AuditAccountUpdated).Take(&audit).Error)
	require.Equal(t, string(sharedtelemetry.ActorKindMember), audit.ActorKind)
	require.Equal(t, admin.MemberID, audit.ActorMemberID)
	require.Equal(t, string(sharedtelemetry.AuditStateNone), audit.PreviousState)
	require.Equal(t, string(sharedtelemetry.AuditStateScheduled), audit.NewState)
}

type failingUnonboardedDeleteAuditWriter struct{}

func (failingUnonboardedDeleteAuditWriter) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("audit unavailable")
}

func TestUnonboardedDeleteAuditFailureRollsBackQueueInsert(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	require.NoError(t, stack.DB.Exec("UPDATE member SET onboarded = FALSE WHERE id = ?::uuid", target.MemberID).Error)
	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	require.NoError(t, testutil.PurgePGMQQueue(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity))

	accepted, err := EnqueueUnonboardedMemberHardDelete(
		auditedOryMemberContext(t, admin),
		stack.DB,
		publisher,
		memberDeletionIntegrationAdapter{},
		failingUnonboardedDeleteAuditWriter{},
		target.MemberID,
	)
	require.False(t, accepted)
	require.ErrorContains(t, err, "audit unavailable")
	messages, err := testutil.ReadPGMQ(t.Context(), sqlDB, eventpkg.QueueUserDeleteIdentity, time.Minute, 1)
	require.NoError(t, err)
	require.Empty(t, messages)
}
