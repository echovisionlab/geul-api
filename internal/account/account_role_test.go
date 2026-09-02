//go:build integration

package account

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestSetAccountRoleRejectsSelfBeforeMutation(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	memberID := actor.MemberID
	ctx := auth.WithUser(context.Background(), actor.AuthUserInfo())
	service := NewAccountService(
		stack.DB, stack.KratosClient, stack.SpiceDBClient, "https://www.example.test", accountRoleNoopLifecyclePublisher{},
		WithMemberEmailProjection(memberEmailProjectionStub{}),
	)

	response, err := service.SetAccountRole(ctx, connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: memberID,
		Role:     policyv1.AuthorizationRole_USER,
	}))

	require.Nil(t, response)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "cannot change your own role")
}
