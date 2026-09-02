//go:build integration

package authentication_test

import (
	"testing"

	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/member"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestRegistrationLoginHookDoesNotPublishWelcomeIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	registered := stack.CreateUser(t, policyv1.Role.User().ID())
	roles := accountadapter.AccountDirectRoleTransition{}
	members := member.NewMemberProvisioner(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		memberadapter.AccountEmailProjection{},
		roles,
	)
	lifecycle := authentication.NewLoginHookService(
		stack.KratosClient,
		authenticationadapter.NewLoginMemberProvisioner(members),
		authentication.NewAuthBootstrapService(
			stack.DB,
			stack.SpiceDBClient,
			apitelemetry.NewDurableWriter(stack.DB),
			roles,
		),
	)

	result, err := lifecycle.Process(t.Context(), authentication.LoginHookInput{
		IdentityID: registered.IdentityID, Email: registered.Email,
		PreferredLocale: "ko", Trigger: "registration",
	})

	require.NoError(t, err)
	require.True(t, result.NewUser)
	require.Equal(t, registered.MemberID, result.MemberID)
	// Registration has no mail publisher dependency. Welcome is owned only by
	// the later Member onboarding transition.
}
