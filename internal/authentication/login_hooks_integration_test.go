//go:build integration

package authentication_test

import (
	"context"
	"errors"
	"testing"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"

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
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	registered := stack.CreateUser(t, policyv1.Role.User().ID())
	roles := accountadapter.AccountDirectRoleTransition{}
	emails := memberadapter.AccountEmailProjection{DB: stack.DB, Identity: stack.KratosClient}
	memberUnits, err := memberpostgres.NewUnits(stack.DB, memberpostgres.Ports{WelcomeEmails: unusedWelcomeEmails{}, WelcomeRecipients: emails})
	require.NoError(t, err)
	members := member.NewMemberProvisioner(memberUnits, stack.KratosClient, stack.SpiceDBClient, emails, roles)
	loginRoles, err := authentication.NewService(authentication.Deps{
		DB: stack.DB, SpiceDB: stack.SpiceDBClient, Audit: apitelemetry.NewDurableWriter(stack.DB), Roles: roles,
	})
	require.NoError(t, err)
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
