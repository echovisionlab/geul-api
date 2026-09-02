//go:build integration

package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func TestAccountAuditRecordsOnlyExactAuthorityTransitionsIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	writer := apitelemetry.NewDurableWriter(stack.DB)
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		writer,
		WithMemberDeletion(memberDeletionIntegrationAdapter{}),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)
	ctx := auditedOryMemberContext(t, actor)

	roleRequest := connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: target.MemberID,
		Role:     policyv1.AuthorizationRole_AUTHOR,
	})
	_, err := service.SetAccountRole(ctx, roleRequest)
	require.NoError(t, err)
	_, err = service.SetAccountRole(ctx, roleRequest)
	require.NoError(t, err)

	banRequest := connect.NewRequest(&managev1.BanAccountRequest{MemberId: target.MemberID})
	_, err = service.BanAccount(ctx, banRequest)
	require.NoError(t, err)
	_, err = service.BanAccount(ctx, banRequest)
	require.NoError(t, err)

	unbanRequest := connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: target.MemberID})
	_, err = service.UnbanAccount(ctx, unbanRequest)
	require.NoError(t, err)
	_, err = service.UnbanAccount(ctx, unbanRequest)
	require.NoError(t, err)

	expiresAt := time.Now().UTC().Add(-time.Minute)
	_, err = service.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{
		MemberId: target.MemberID,
		Until:    timestamppb.New(expiresAt),
	}))
	require.NoError(t, err)
	cleared, err := ClearExpiredTimedBan(
		t.Context(), stack.DB, stack.KratosClient, target.IdentityID, time.Now().UTC(), writer,
	)
	require.NoError(t, err)
	require.True(t, cleared)
	_, err = service.DeleteAccount(ctx, connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: target.MemberID}))
	require.NoError(t, err)

	var records []struct {
		Action        string
		ActorKind     string
		ActorMemberID *string
		ActorService  *string
		TargetID      string
		ChangedFields pq.StringArray `gorm:"type:text[]"`
		PreviousState *string
		NewState      *string
		PreviousRole  *string
		NewRole       *string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id,
		       actor_service, target_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'previous_state' AS previous_state,
		       attributes->>'new_state' AS new_state,
		       attributes->>'previous_role' AS previous_role,
		       attributes->>'new_role' AS new_role
		FROM public.domain_audit
		WHERE target_type = 'member' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, target.MemberID).Scan(&records).Error)
	require.Len(t, records, 5)
	require.Equal(t, string(sharedtelemetry.AuditMemberUpdated), records[0].Action)
	require.Equal(t, policyv1.Role.User().ID(), requireStringPointer(t, records[0].PreviousRole))
	require.Equal(t, policyv1.Role.Author().ID(), requireStringPointer(t, records[0].NewRole))
	require.Equal(t, pq.StringArray{"role"}, records[0].ChangedFields)
	require.Nil(t, records[0].PreviousState)
	require.Nil(t, records[0].NewState)
	for index, transition := range [][2]sharedtelemetry.AuditState{
		{sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateBanned},
		{sharedtelemetry.AuditStateBanned, sharedtelemetry.AuditStateActive},
		{sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateBanned},
		{sharedtelemetry.AuditStateBanned, sharedtelemetry.AuditStateActive},
	} {
		record := records[index+1]
		require.Equal(t, string(sharedtelemetry.AuditMemberUpdated), record.Action)
		require.Equal(t, pq.StringArray{"status"}, record.ChangedFields)
		require.Equal(t, string(transition[0]), requireStringPointer(t, record.PreviousState))
		require.Equal(t, string(transition[1]), requireStringPointer(t, record.NewState))
		require.Nil(t, record.PreviousRole)
		require.Nil(t, record.NewRole)
	}
	for _, record := range records[:4] {
		require.Equal(t, string(sharedtelemetry.ActorKindMember), record.ActorKind)
		require.Equal(t, actor.MemberID, requireStringPointer(t, record.ActorMemberID))
		require.Nil(t, record.ActorService)
	}
	require.Equal(t, string(sharedtelemetry.ActorKindSystem), records[4].ActorKind)
	require.Nil(t, records[4].ActorMemberID)
	require.Equal(t, "geul-backend", requireStringPointer(t, records[4].ActorService))

	var deletionAudit struct {
		Action        string
		ActorMemberID string
		ChangedFields pq.StringArray `gorm:"type:text[]"`
		PreviousState string
		NewState      string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'previous_state' AS previous_state,
		       attributes->>'new_state' AS new_state
		FROM public.domain_audit
		WHERE target_type = 'account' AND target_id = ?
	`, target.MemberID).Take(&deletionAudit).Error)
	require.Equal(t, string(sharedtelemetry.AuditAccountUpdated), deletionAudit.Action)
	require.Equal(t, actor.MemberID, deletionAudit.ActorMemberID)
	require.Equal(t, pq.StringArray{"deletion_state"}, deletionAudit.ChangedFields)
	require.Equal(t, string(sharedtelemetry.AuditStateNone), deletionAudit.PreviousState)
	require.Equal(t, string(sharedtelemetry.AuditStateScheduled), deletionAudit.NewState)
}

type failingAccountRoleAuditAppender struct{}

func (failingAccountRoleAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("audit unavailable")
}

func TestSetAccountRoleRestoresDirectRoleWhenAuditFailsIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		failingAccountRoleAuditAppender{},
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	response, err := service.SetAccountRole(
		auditedOryMemberContext(t, actor),
		connect.NewRequest(&managev1.SetAccountRoleRequest{
			MemberId: target.MemberID,
			Role:     policyv1.AuthorizationRole_AUTHOR,
		}),
	)
	require.Nil(t, response)
	require.ErrorContains(t, err, "audit unavailable")
	requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsUser, true)
	requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsAuthor, false)
}

func TestSetAccountRoleRevalidatesActorLifecycleUnderIdentityFencesIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		invalidate func(t *testing.T, stack *testutil.OryStack, actor *testutil.OryUser)
	}{
		{
			name: "banned",
			invalidate: func(t *testing.T, stack *testutil.OryStack, actor *testutil.OryUser) {
				require.NoError(t, stack.KratosClient.UpdateIdentityMetadataAdmin(
					t.Context(), actor.IdentityID, structured.Fields{"banned": true},
				))
			},
		},
		{
			name: "inactive",
			invalidate: func(t *testing.T, stack *testutil.OryStack, actor *testutil.OryUser) {
				require.NoError(t, stack.KratosClient.SetIdentityState(t.Context(), actor.IdentityID, auth.KratosStateInactive))
			},
		},
		{
			name: "pending_deletion",
			invalidate: func(t *testing.T, stack *testutil.OryStack, actor *testutil.OryUser) {
				now := time.Now().UTC()
				require.NoError(t, stack.DB.Create(&model.UserDeletionRequest{
					ID: uuid.NewString(), MemberID: actor.MemberID, IdentityID: actor.IdentityID,
					Token: uuid.NewString(), TokenExpiresAt: now.Add(time.Hour),
					LifecycleState: accountLifecycleStateScheduled, ScheduledAt: &now,
					CreatedAt: now, UpdatedAt: now,
				}).Error)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stack := testutil.SetupOryStack(t)
			actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
			target := stack.CreateUser(t, policyv1.Role.User().ID())
			service := NewAccountService(
				stack.DB,
				stack.KratosClient,
				stack.SpiceDBClient,
				"https://www.example.test",
				accountRoleNoopLifecyclePublisher{},
				WithMemberEmailProjection(memberEmailProjectionIntegration{}),
			)
			testCase.invalidate(t, stack, actor)

			response, err := service.SetAccountRole(
				auth.WithUser(t.Context(), actor.AuthUserInfo()),
				connect.NewRequest(&managev1.SetAccountRoleRequest{
					MemberId: target.MemberID,
					Role:     policyv1.AuthorizationRole_AUTHOR,
				}),
			)
			require.Nil(t, response)
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsUser, true)
			requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsAuthor, false)
		})
	}
}

func TestSetAccountRoleSynchronizesSpiceDBRoleAndWritesAuditIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	_, err := service.SetAccountRole(
		auditedOryMemberContext(t, actor),
		connect.NewRequest(&managev1.SetAccountRoleRequest{
			MemberId: target.MemberID,
			Role:     policyv1.AuthorizationRole_AUTHOR,
		}),
	)
	require.NoError(t, err)
	requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsAuthor, true)

	var auditCount int64
	require.NoError(t, stack.DB.Table("public.domain_audit").
		Where("action = ? AND target_type = ? AND target_id = ?", sharedtelemetry.AuditMemberUpdated, "member", target.MemberID).
		Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestSetAccountRoleSynchronizesEveryDirectRoleTransitionIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	for _, transition := range []struct {
		name string
		from policyv1.RoleID
		to   policyv1.AuthorizationRole
		want policyv1.RoleID
	}{
		{"user_to_author", policyv1.Role.User(), policyv1.AuthorizationRole_AUTHOR, policyv1.Role.Author()},
		{"user_to_admin", policyv1.Role.User(), policyv1.AuthorizationRole_ADMIN, policyv1.Role.Admin()},
		{"author_to_user", policyv1.Role.Author(), policyv1.AuthorizationRole_USER, policyv1.Role.User()},
		{"author_to_admin", policyv1.Role.Author(), policyv1.AuthorizationRole_ADMIN, policyv1.Role.Admin()},
		{"admin_to_user", policyv1.Role.Admin(), policyv1.AuthorizationRole_USER, policyv1.Role.User()},
		{"admin_to_author", policyv1.Role.Admin(), policyv1.AuthorizationRole_AUTHOR, policyv1.Role.Author()},
	} {
		t.Run(transition.name, func(t *testing.T) {
			target := stack.CreateUser(t, transition.from.ID())
			_, err := service.SetAccountRole(
				auditedOryMemberContext(t, actor),
				connect.NewRequest(&managev1.SetAccountRoleRequest{MemberId: target.MemberID, Role: transition.to}),
			)
			require.NoError(t, err)
			subject := requireAccountIdentitySubject(t, target.IdentityID)
			actual, found, err := stack.SpiceDBClient.ReadDirectGlobalRole(t.Context(), subject)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, transition.want, actual)
		})
	}
}

func TestSetAccountRoleSameRoleDoesNotWriteAuditIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.Author().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	_, err := service.SetAccountRole(
		auditedOryMemberContext(t, actor),
		connect.NewRequest(&managev1.SetAccountRoleRequest{
			MemberId: target.MemberID,
			Role:     policyv1.AuthorizationRole_AUTHOR,
		}),
	)
	require.NoError(t, err)
	requireGlobalRolePermission(t, t.Context(), stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsAuthor, true)

	var auditCount int64
	require.NoError(t, stack.DB.Table("public.domain_audit").
		Where("action = ? AND target_type = ? AND target_id = ?", sharedtelemetry.AuditMemberUpdated, "member", target.MemberID).
		Count(&auditCount).Error)
	require.Zero(t, auditCount)
}

type closingAccountRoleAuditAppender struct {
	spicedb *auth.SpiceDBClient
}

func (w closingAccountRoleAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.Join(errors.New("audit unavailable"), w.spicedb.Close())
}

func TestSetAccountRoleSurfacesSpiceDBRestoreFailureIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	roleClient, err := auth.NewSpiceDBClient(stack.SpiceDBEndpoint, stack.SpiceDBToken, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = roleClient.Close() })
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		roleClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		closingAccountRoleAuditAppender{spicedb: roleClient},
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	_, err = service.SetAccountRole(
		auditedOryMemberContext(t, actor),
		connect.NewRequest(&managev1.SetAccountRoleRequest{
			MemberId: target.MemberID,
			Role:     policyv1.AuthorizationRole_AUTHOR,
		}),
	)
	require.ErrorContains(t, err, "audit unavailable")
	// SpiceDB compensation runs while the database transaction still holds its
	// locks, before the failed transaction rolls back. This keeps an uncertain
	// relationship write from becoming visible to a concurrent transaction.
	require.ErrorContains(t, err, "compensate authorization relationships before database rollback")
}

func auditedOryMemberContext(t *testing.T, user *testutil.OryUser) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.88")
	require.NoError(t, err)
	return auth.WithUser(
		sharedtelemetry.WithRequestContext(t.Context(), requestContext),
		user.AuthUserInfo(),
	)
}

func requireStringPointer(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}
