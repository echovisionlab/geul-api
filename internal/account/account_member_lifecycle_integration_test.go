//go:build integration

package account

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAccountRoleMutationKeepsUnonboardedMemberOutOfLastAdminCountIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	roleTarget := stack.CreateUser(t, policyv1.Role.User().ID())
	falseAdmin := stack.CreateUser(t, policyv1.Role.Author().ID())
	falseAdminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(falseAdmin.IdentityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), falseAdminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	require.NoError(t, stack.DB.Exec(
		`UPDATE member SET onboarded=FALSE WHERE id IN (?::uuid, ?::uuid)`, roleTarget.MemberID, falseAdmin.MemberID,
	).Error)

	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)
	ctx := auditedOryMemberContext(t, actor)
	response, err := service.SetAccountRole(
		ctx,
		connect.NewRequest(&managev1.SetAccountRoleRequest{
			MemberId: roleTarget.MemberID,
			Role:     policyv1.AuthorizationRole_AUTHOR,
		}),
	)
	require.NoError(t, err)
	require.Equal(t, policyv1.AuthorizationRole_AUTHOR, response.Msg.Role)
	var auditCount int64
	require.NoError(t, stack.DB.Table("public.domain_audit").
		Where(
			"action = ? AND target_type = ? AND target_id = ?",
			sharedtelemetry.AuditMemberUpdated,
			"member",
			roleTarget.MemberID,
		).
		Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
	requireMemberOnboarded(t, stack.DB, roleTarget.MemberID, false)

	_, err = service.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: roleTarget.MemberID}))
	require.NoError(t, err)
	requireMemberOnboarded(t, stack.DB, roleTarget.MemberID, false)
	_, err = service.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: roleTarget.MemberID}))
	require.NoError(t, err)
	requireMemberOnboarded(t, stack.DB, roleTarget.MemberID, false)
	_, err = service.SetAccountCanonicalEmail(
		ctx,
		connect.NewRequest(&managev1.SetAccountCanonicalEmailRequest{MemberId: roleTarget.MemberID, Email: roleTarget.Email}),
	)
	require.NoError(t, err)
	requireMemberOnboarded(t, stack.DB, roleTarget.MemberID, false)
	_, err = service.RemoveAccountSsoProvider(
		ctx,
		connect.NewRequest(&managev1.RemoveAccountSsoProviderRequest{
			MemberId:   roleTarget.MemberID,
			Provider:   "google",
			Identifier: "missing-provider-subject",
		}),
	)
	require.NoError(t, err)
	requireMemberOnboarded(t, stack.DB, roleTarget.MemberID, false)

	err = stack.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		return ValidateLastActiveAdminDeletionWithAuthorization(
			t.Context(), tx, actor.MemberID, actor.IdentityID, stack.SpiceDBClient,
		)
	})
	require.ErrorIs(t, err, ErrLastActiveAdminDeletion)
}

func requireMemberOnboarded(t *testing.T, db *gorm.DB, memberID string, want bool) {
	t.Helper()
	var onboarded bool
	require.NoError(t, db.Raw(`SELECT onboarded FROM member WHERE id=?::uuid`, memberID).Scan(&onboarded).Error)
	require.Equal(t, want, onboarded)
}
