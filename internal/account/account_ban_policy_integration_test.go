//go:build integration

package account

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
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

func (accountBanNoopLifecyclePublisher) PublishUserDeleteIdentityWithExecutor(
	context.Context,
	eventpkg.DBTX,
	*managev1.UserDeleteIdentityCommand,
) error {
	return nil
}

func TestBanAccountRejectsAdminTargetWithoutIdentityMutationIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.Admin().ID())
	service := newTestHandler(t, Deps{DB: stack.DB, Identity: stack.KratosClient, SpiceDB: stack.SpiceDBClient, Publisher: accountBanNoopLifecyclePublisher{}, Audit: apitelemetry.NewDurableWriter(stack.DB), MemberEmails: memberEmailProjectionIntegration{}, SiteOrigin: "https://www.example.test"})
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
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	service := newTestHandler(t, Deps{DB: stack.DB, Identity: stack.KratosClient, SpiceDB: stack.SpiceDBClient, Publisher: accountBanNoopLifecyclePublisher{}, Audit: apitelemetry.NewDurableWriter(stack.DB), MemberEmails: memberEmailProjectionIntegration{}, SiteOrigin: "https://www.example.test"})
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

func TestUnbanAccountRejectsPendingDeletionIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	service := newTestHandler(t, Deps{DB: stack.DB, Identity: stack.KratosClient, SpiceDB: stack.SpiceDBClient, Publisher: accountBanNoopLifecyclePublisher{}, Audit: apitelemetry.NewDurableWriter(stack.DB), MemberEmails: memberEmailProjectionIntegration{}})
	ctx := auditedOryMemberContext(t, actor)
	_, err := service.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: target.MemberID}))
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, stack.DB.Create(&model.UserDeletionRequest{
		ID: uuid.NewString(), MemberID: target.MemberID, IdentityID: target.IdentityID,
		Token: uuid.NewString(), TokenExpiresAt: now.Add(time.Hour),
		LifecycleState: accountLifecycleStateScheduled, ScheduledAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	response, err := service.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: target.MemberID}))

	require.Nil(t, response)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.ErrorContains(t, err, "an account pending deletion cannot be unbanned")
	identity, err := stack.KratosClient.GetIdentity(t.Context(), target.IdentityID)
	require.NoError(t, err)
	require.True(t, identity.IsBanned())
}
