//go:build integration

package authentication_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// The registration hook end to end: the Identity read from real Kratos,
// Member provisioning, and the login role sync on PostgreSQL and SpiceDB.
func TestRegistrationLoginHookDoesNotPublishWelcomeIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	registered := stack.CreateUser(t, policyv1.Role.User().ID())
	roles := accountadapter.AccountDirectRoleTransition{}
	accountUnits, err := accountpostgres.NewEmailProjectionUnits(stack.DB, accountadapter.MemberEmailProjection{})
	require.NoError(t, err)
	emails := memberadapter.AccountEmailProjection{Units: accountUnits, Identity: stack.KratosClient}
	memberUnits, err := memberpostgres.NewUnits(stack.DB, memberpostgres.Ports{WelcomeEmails: unusedWelcomeEmails{}, WelcomeRecipients: emails})
	require.NoError(t, err)
	members := member.NewMemberProvisioner(memberUnits, stack.KratosClient, stack.SpiceDBClient, emails, roles)
	loginRoles := newIntegrationLoginRoles(t, stack, apitelemetry.NewDurableWriter(stack.DB))
	lifecycle := authentication.NewLoginHookService(
		stack.KratosClient,
		authenticationadapter.NewLoginMemberProvisioner(members),
		loginRoles,
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

// unusedWelcomeEmails fails a welcome enqueue, which registration never
// reaches.
type unusedWelcomeEmails struct{}

func (unusedWelcomeEmails) PublishSendEmailWithExecutor(context.Context, eventpkg.DBTX, *managev1.SendEmailEvent) error {
	return errors.New("registration does not enqueue a welcome email")
}
