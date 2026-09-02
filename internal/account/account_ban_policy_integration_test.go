//go:build integration

package account

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

type accountBanNoopLifecyclePublisher struct{}

func (accountBanNoopLifecyclePublisher) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return nil
}

func (accountBanNoopLifecyclePublisher) PublishUserDeleteIdentity(
	context.Context,
	*managev1.UserDeleteIdentityCommand,
) error {
	return nil
}

func (accountBanNoopLifecyclePublisher) PublishUserDeleteAvatar(
	context.Context,
	*managev1.UserDeleteAvatarCommand,
) error {
	return nil
}

func TestBanAccountRejectsAdminTargetWithoutIdentityMutationIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.Admin().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountBanNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)
	ctx := auditedOryMemberContext(t, actor)

	response, err := service.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{
		MemberId: target.MemberID,
	}))

	require.Nil(t, response)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.ErrorContains(t, err, "admin accounts cannot be banned")

	targetIdentity, err := stack.KratosClient.GetIdentity(t.Context(), target.IdentityID)
	require.NoError(t, err)
	require.Equal(t, auth.KratosStateActive, targetIdentity.State)
	requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsAdmin, true)
	require.False(t, targetIdentity.IsBanned())
}

func TestBanAccountRejectsTargetAfterRolePromotionIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountBanNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)
	ctx := auditedOryMemberContext(t, actor)

	_, err := service.SetAccountRole(ctx, connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: target.MemberID,
		Role:     policyv1.AuthorizationRole_ADMIN,
	}))
	require.NoError(t, err)
	_, err = service.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{
		MemberId: target.MemberID,
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	targetIdentity, err := stack.KratosClient.GetIdentity(t.Context(), target.IdentityID)
	require.NoError(t, err)
	require.Equal(t, auth.KratosStateActive, targetIdentity.State)
	requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsAdmin, true)
	require.False(t, targetIdentity.IsBanned())
}
