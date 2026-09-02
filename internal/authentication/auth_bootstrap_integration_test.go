//go:build integration

package authentication

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuthBootstrapServiceSynchronizesMissingRolesWithoutDowngradingExistingRolesIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	testutil.ResetFirstAdminBootstrapFixture(t, stack.DB)
	service := NewAuthBootstrapService(
		stack.DB,
		stack.SpiceDBClient,
		apitelemetry.NewDurableWriter(stack.DB),
		integrationDirectRoleTransition{},
	)
	ctx := context.Background()

	first := stack.CreateUser(t, policyv1.Role.User().ID())
	firstResult, err := service.SyncLoginRole(ctx, first.IdentityID, first.MemberID)
	require.NoError(t, err)
	require.True(t, firstResult.FirstAdmin)
	require.Equal(t, policyv1.Role.Admin(), firstResult.Role)
	requireGlobalRolePermission(t, ctx, stack.SpiceDBClient, first.IdentityID, policyv1.Platform.IsAdmin, true)

	fresh := stack.CreateUser(t, policyv1.Role.User().ID())
	freshSubject := requireAccountIdentitySubject(t, fresh.IdentityID)
	_, err = stack.SpiceDBClient.DeleteAllAccountIdentityRelationships(ctx, freshSubject)
	require.NoError(t, err)
	freshResult, err := service.SyncLoginRole(ctx, fresh.IdentityID, fresh.MemberID)
	require.NoError(t, err)
	require.False(t, freshResult.FirstAdmin)
	require.Equal(t, policyv1.Role.User(), freshResult.Role)
	requireGlobalRolePermission(t, ctx, stack.SpiceDBClient, fresh.IdentityID, policyv1.Platform.IsUser, true)
	requireGlobalRolePermission(t, ctx, stack.SpiceDBClient, fresh.IdentityID, policyv1.Platform.IsAdmin, false)

	repeatResult, err := service.SyncLoginRole(ctx, fresh.IdentityID, fresh.MemberID)
	require.NoError(t, err)
	require.Equal(t, policyv1.Role.User(), repeatResult.Role)
	requireGlobalRolePermission(t, ctx, stack.SpiceDBClient, fresh.IdentityID, policyv1.Platform.IsUser, true)

	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	adminResult, err := service.SyncLoginRole(ctx, admin.IdentityID, admin.MemberID)
	require.NoError(t, err)
	require.Equal(t, policyv1.Role.Admin(), adminResult.Role)
	requireGlobalRolePermission(t, ctx, stack.SpiceDBClient, admin.IdentityID, policyv1.Platform.IsAdmin, true)

	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	authorResult, err := service.SyncLoginRole(ctx, author.IdentityID, author.MemberID)
	require.NoError(t, err)
	require.Equal(t, policyv1.Role.Author(), authorResult.Role)
	requireGlobalRolePermission(t, ctx, stack.SpiceDBClient, author.IdentityID, policyv1.Platform.IsAuthor, true)
	requireGlobalRolePermission(t, ctx, stack.SpiceDBClient, author.IdentityID, policyv1.Platform.IsAdmin, false)
}

type failingAuthBootstrapAuditAppender struct{}

func (failingAuthBootstrapAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("bootstrap audit unavailable")
}

func TestAuthBootstrapServiceRestoresDirectRoleAfterConfirmedCallbackRollbackIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	testutil.ResetFirstAdminBootstrapFixture(t, stack.DB)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	subject := requireAccountIdentitySubject(t, user.IdentityID)
	_, err := stack.SpiceDBClient.DeleteAllAccountIdentityRelationships(t.Context(), subject)
	require.NoError(t, err)
	service := NewAuthBootstrapService(stack.DB, stack.SpiceDBClient, failingAuthBootstrapAuditAppender{}, integrationDirectRoleTransition{})

	result, err := service.SyncLoginRole(t.Context(), user.IdentityID, user.MemberID)
	require.Empty(t, result)
	require.ErrorContains(t, err, "bootstrap audit unavailable")
	_, found, err := stack.SpiceDBClient.ReadDirectGlobalRole(t.Context(), subject)
	require.NoError(t, err)
	require.False(t, found)
}

type integrationDirectRoleTransition struct{}

func (integrationDirectRoleTransition) Transition(
	subject auth.AccountIdentitySubject,
	desired policyv1.RoleID,
	previous policyv1.RoleID,
	previousFound bool,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply, err := account.RoleReplacementMutations(subject, desired)
	if err != nil {
		return nil, nil, err
	}
	compensate, err := account.RoleRestoreMutations(subject, previous, previousFound)
	if err != nil {
		return nil, nil, err
	}
	return apply, compensate, nil
}

func requireAccountIdentitySubject(t *testing.T, identityID string) auth.AccountIdentitySubject {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	return subject
}

func requireGlobalRolePermission(
	t *testing.T,
	ctx context.Context,
	spicedb *auth.SpiceDBClient,
	identityID string,
	canFor func() (policyv1.Can, error),
	want bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := canFor()
	require.NoError(t, err)
	allowed, err := spicedb.CheckActorCan(ctx, actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed, can.Action().Name())
}
