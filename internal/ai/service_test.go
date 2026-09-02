//go:build integration

package ai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestCanUseAIRequiresAdminForWork(t *testing.T) {
	target := &managev1.AIResourceTarget{
		Type: managev1.AIResourceType_AI_RESOURCE_TYPE_WORK,
		Id:   "00000000-0000-4000-8000-000000000001",
	}
	stack := testutil.SetupOryStack(t)
	authorUser := stack.CreateUser(t, policyv1.Role.Author().ID())
	author := authorUser.AuthUserInfo()
	allowed, err := canUseAI(auth.WithUser(context.Background(), author), stack.SpiceDBClient, author, target)
	require.NoError(t, err)
	require.False(t, allowed)

	adminUser := stack.CreateUser(t, policyv1.Role.Admin().ID())
	admin := adminUser.AuthUserInfo()
	allowed, err = canUseAI(auth.WithUser(context.Background(), admin), stack.SpiceDBClient, admin, target)
	require.NoError(t, err)
	require.True(t, allowed)
}
