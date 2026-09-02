//go:build integration

package account

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

type accountRoleNoopLifecyclePublisher struct{}

func (accountRoleNoopLifecyclePublisher) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return nil
}

func (accountRoleNoopLifecyclePublisher) PublishUserDeleteIdentity(
	context.Context,
	*managev1.UserDeleteIdentityCommand,
) error {
	return nil
}

func (accountRoleNoopLifecyclePublisher) PublishUserDeleteAvatar(
	context.Context,
	*managev1.UserDeleteAvatarCommand,
) error {
	return nil
}

func (accountRoleNoopLifecyclePublisher) PublishUserDeleteIdentityWithExecutor(
	context.Context,
	eventpkg.DBTX,
	*managev1.UserDeleteIdentityCommand,
) error {
	return nil
}

func (accountRoleNoopLifecyclePublisher) PublishUserDeleteAvatarWithExecutor(
	context.Context,
	eventpkg.DBTX,
	*managev1.UserDeleteAvatarCommand,
) error {
	return nil
}

func TestSetAccountRoleConcurrentPeerDemotionKeepsOneActiveAdminIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	first := stack.CreateUser(t, policyv1.Role.Admin().ID())
	second := stack.CreateUser(t, policyv1.Role.Admin().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := service.SetAccountRole(
			auditedOryMemberContext(t, first),
			connect.NewRequest(&managev1.SetAccountRoleRequest{
				MemberId: second.MemberID,
				Role:     policyv1.AuthorizationRole_USER,
			}),
		)
		results <- err
	}()
	go func() {
		<-start
		_, err := service.SetAccountRole(
			auditedOryMemberContext(t, second),
			connect.NewRequest(&managev1.SetAccountRoleRequest{
				MemberId: first.MemberID,
				Role:     policyv1.AuthorizationRole_USER,
			}),
		)
		results <- err
	}()
	close(start)

	var successes, denied int
	for range 2 {
		result := <-results
		if result == nil {
			successes++
		} else if connect.CodeOf(result) == connect.CodePermissionDenied {
			denied++
		} else {
			t.Fatalf("unexpected concurrent role mutation result: %v", result)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, denied)
	var auditCount int64
	require.NoError(t, stack.DB.Table("public.domain_audit").
		Where("action = ? AND target_type = ? AND target_id IN (?, ?)", sharedtelemetry.AuditMemberUpdated, "member", first.MemberID, second.MemberID).
		Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)

	activeAdmins := 0
	can, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	for _, account := range []*testutil.OryUser{first, second} {
		actor, err := policyv1.NewAccountIdentityActor(account.IdentityID)
		require.NoError(t, err)
		admin, err := stack.SpiceDBClient.CheckActorCan(t.Context(), actor, can)
		require.NoError(t, err)
		if admin {
			activeAdmins++
		}
	}
	require.Equal(t, 1, activeAdmins)
}
