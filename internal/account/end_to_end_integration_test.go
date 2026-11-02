//go:build integration

package account_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"

	// These tests run the Account use cases over the production Store, a real
	// Kratos and a real SpiceDB, for what the fakes cannot answer: the identity
	// fence over Kratos rows, SpiceDB's direct-role relationships and their
	// read-after-write consistency, the last-admin fence under concurrency, and
	// the deletion worker across Kratos, SpiceDB and PostgreSQL.
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

func TestMain(m *testing.M) {
	suite, err := orytest.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start account Ory integration suite:", err)
		os.Exit(1)
	}
	orytest.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	orytest.DeactivateOryIntegrationSuite(suite)
	if closeErr := suite.Close(); closeErr != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "cleanup account Ory integration suite:", closeErr)
		code = 1
	}
	os.Exit(code)
}

// integrationService is the production composition of the Account use cases
// over stack.
func integrationService(t *testing.T, stack *orytest.OryStack) (*account.Service, account.Units, *mq.Publisher) {
	t.Helper()
	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	units, err := accountpostgres.NewUnits(stack.DB, accountpostgres.Ports{
		Members: accountadapter.MemberDeletion{}, MemberEmails: accountadapter.MemberEmailProjection{},
		Newsletter: accountadapter.NewsletterSubscription{}, Publisher: publisher,
	})
	require.NoError(t, err)
	service, err := account.NewService(account.Deps{
		Units: units, SpiceDB: stack.SpiceDBClient, Fence: identitystate.PostgresFence{}, Identity: stack.KratosClient,
		Emails: &accounttest.Emails{}, Members: accountpostgres.MemberAuthorizations(stack.DB, accountadapter.MemberDeletion{}, stack.SpiceDBClient),
		Audit: apitelemetry.NewDurableWriter(stack.DB), SiteOrigin: "https://www.example.test",
	})
	require.NoError(t, err)
	return service, units, publisher
}

func auditedOryMemberContext(t *testing.T, user *orytest.OryUser) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.88")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), user.AuthUserInfo())
}

func requireGlobalRolePermission(t *testing.T, spicedb *auth.SpiceDBClient, identityID string, canFor func() (policyv1.Can, error), want bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := canFor()
	require.NoError(t, err)
	allowed, err := spicedb.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed, can.Action().Name())
}

func requireAdminRequired(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, "admin access required", connectErr.Message())
}

// A deactivated or banned administrator with a live session and the SpiceDB
// role changes nothing: every administrator mutation checks the identity
// fence over the real Kratos rows.
func TestAccountAdminMutationsRejectStaleAdministratorIdentityIntegration(t *testing.T) {
	t.Parallel()
	for name, stale := range map[string]func(*testing.T, *orytest.OryStack, *orytest.OryUser){
		"deactivated identity": func(t *testing.T, stack *orytest.OryStack, admin *orytest.OryUser) {
			require.NoError(t, stack.KratosClient.SetIdentityState(t.Context(), admin.IdentityID, auth.KratosStateInactive))
		},
		"banned identity": func(t *testing.T, stack *orytest.OryStack, admin *orytest.OryUser) {
			require.NoError(t, stack.KratosClient.UpdateIdentityMetadataAdmin(t.Context(), admin.IdentityID, structured.Fields{"banned": true}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stack := orytest.SetupOryStack(t)
			admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
			// A second administrator keeps the last-admin fence out of the way.
			stack.CreateUser(t, policyv1.Role.Admin().ID())
			target := stack.CreateUser(t, policyv1.Role.User().ID())
			service, _, _ := integrationService(t, stack)
			handler := account.NewConnectHandler(service)
			ctx := auditedOryMemberContext(t, admin)
			stale(t, stack, admin)

			_, err := handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)
			_, err = handler.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)
			_, err = handler.SetAccountCanonicalEmail(ctx, connect.NewRequest(&managev1.SetAccountCanonicalEmailRequest{
				MemberId: target.MemberID, Email: "stale-admin-" + target.Email,
			}))
			requireAdminRequired(t, err)
			_, err = handler.RemoveAccountSsoProvider(ctx, connect.NewRequest(&managev1.RemoveAccountSsoProviderRequest{
				MemberId: target.MemberID, Provider: "google", Identifier: "stale-admin-subject",
			}))
			requireAdminRequired(t, err)
			_, err = handler.UnsubscribeAccountFromNewsletter(ctx, connect.NewRequest(&managev1.UnsubscribeAccountFromNewsletterRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)
			_, err = handler.DeleteAccount(ctx, connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: target.MemberID}))
			requireAdminRequired(t, err)

			identity, err := stack.KratosClient.GetIdentity(t.Context(), target.IdentityID)
			require.NoError(t, err)
			require.Equal(t, auth.KratosStateActive, identity.State)
			require.False(t, identity.IsBanned())
			var requests, audits int64
			require.NoError(t, stack.DB.Model(&model.UserDeletionRequest{}).Where("member_id = ?::uuid", target.MemberID).Count(&requests).Error)
			require.Zero(t, requests)
			require.NoError(t, stack.DB.Table("public.domain_audit").Where("target_id = ?", target.MemberID).Count(&audits).Error)
			require.Zero(t, audits)
		})
	}
}

// Role, ban, unban, timed-ban expiry and deletion each record one durable
// row per real transition of the Kratos and SpiceDB state; a repeated
// request records nothing.
func TestAccountAuditRecordsOnlyExactAuthorityTransitionsIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	service, _, _ := integrationService(t, stack)
	handler := account.NewConnectHandler(service)
	writer := apitelemetry.NewDurableWriter(stack.DB)
	ctx := auditedOryMemberContext(t, actor)

	roleRequest := connect.NewRequest(&managev1.SetAccountRoleRequest{MemberId: target.MemberID, Role: policyv1.AuthorizationRole_AUTHOR})
	for range 2 {
		_, err := handler.SetAccountRole(ctx, roleRequest)
		require.NoError(t, err)
	}
	for range 2 {
		_, err := handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: target.MemberID}))
		require.NoError(t, err)
	}
	for range 2 {
		_, err := handler.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: target.MemberID}))
		require.NoError(t, err)
	}
	_, err := handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{
		MemberId: target.MemberID, Until: timestamppb.New(time.Now().UTC().Add(-time.Minute)),
	}))
	require.NoError(t, err)
	cleared, err := accountpostgres.ClearExpiredTimedBan(t.Context(), stack.DB, stack.KratosClient, target.IdentityID, time.Now().UTC(), writer)
	require.NoError(t, err)
	require.True(t, cleared)
	_, err = handler.DeleteAccount(ctx, connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: target.MemberID}))
	require.NoError(t, err)

	var records []struct {
		Action        string
		ActorKind     string
		ActorMemberID *string
		ActorService  *string
		ChangedFields string
		PreviousState *string
		NewState      *string
		PreviousRole  *string
		NewRole       *string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id, actor_service,
		       attributes->>'changed_fields' AS changed_fields,
		       attributes->>'previous_state' AS previous_state, attributes->>'new_state' AS new_state,
		       attributes->>'previous_role' AS previous_role, attributes->>'new_role' AS new_role
		FROM public.domain_audit
		WHERE target_type = 'member' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, target.MemberID).Scan(&records).Error)
	require.Len(t, records, 5)
	require.Equal(t, string(sharedtelemetry.AuditMemberUpdated), records[0].Action)
	require.Equal(t, `["role"]`, records[0].ChangedFields)
	require.Equal(t, policyv1.Role.User().ID(), *records[0].PreviousRole)
	require.Equal(t, policyv1.Role.Author().ID(), *records[0].NewRole)
	require.Nil(t, records[0].PreviousState)
	for index, transition := range [][2]sharedtelemetry.AuditState{
		{sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateBanned},
		{sharedtelemetry.AuditStateBanned, sharedtelemetry.AuditStateActive},
		{sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateBanned},
		{sharedtelemetry.AuditStateBanned, sharedtelemetry.AuditStateActive},
	} {
		record := records[index+1]
		require.Equal(t, `["status"]`, record.ChangedFields)
		require.Equal(t, string(transition[0]), *record.PreviousState)
		require.Equal(t, string(transition[1]), *record.NewState)
		require.Nil(t, record.PreviousRole)
	}
	for _, record := range records[:4] {
		require.Equal(t, string(sharedtelemetry.ActorKindMember), record.ActorKind)
		require.Equal(t, actor.MemberID, *record.ActorMemberID)
		require.Nil(t, record.ActorService)
	}
	require.Equal(t, string(sharedtelemetry.ActorKindSystem), records[4].ActorKind)
	require.Nil(t, records[4].ActorMemberID)
	require.Equal(t, "geul-backend", *records[4].ActorService)

	var deletion struct {
		Action        string
		ActorMemberID string
		ChangedFields string
		PreviousState string
		NewState      string
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id, attributes->>'changed_fields' AS changed_fields,
		       attributes->>'previous_state' AS previous_state, attributes->>'new_state' AS new_state
		FROM public.domain_audit
		WHERE target_type = 'account' AND target_id = ?
	`, target.MemberID).Take(&deletion).Error)
	require.Equal(t, string(sharedtelemetry.AuditAccountUpdated), deletion.Action)
	require.Equal(t, actor.MemberID, deletion.ActorMemberID)
	require.Equal(t, `["deletion_state"]`, deletion.ChangedFields)
	require.Equal(t, string(sharedtelemetry.AuditStateNone), deletion.PreviousState)
	require.Equal(t, string(sharedtelemetry.AuditStateScheduled), deletion.NewState)
}

// Each direct-role transition replaces the relationship SpiceDB reads back.
func TestSetAccountRoleSynchronizesEveryDirectRoleTransitionIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	service, _, _ := integrationService(t, stack)
	handler := account.NewConnectHandler(service)

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
			_, err := handler.SetAccountRole(auditedOryMemberContext(t, actor), connect.NewRequest(&managev1.SetAccountRoleRequest{
				MemberId: target.MemberID, Role: transition.to,
			}))
			require.NoError(t, err)
			subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(target.IdentityID))
			require.NoError(t, err)
			persistedRole, found, err := stack.SpiceDBClient.ReadDirectGlobalRole(t.Context(), subject)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, transition.want, persistedRole)
		})
	}
}

// The ban check reads the role SpiceDB has just written.
func TestBanAccountRejectsTargetAfterRolePromotionIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	service, _, _ := integrationService(t, stack)
	handler := account.NewConnectHandler(service)
	ctx := auditedOryMemberContext(t, actor)

	_, err := handler.SetAccountRole(ctx, connect.NewRequest(&managev1.SetAccountRoleRequest{MemberId: target.MemberID, Role: policyv1.AuthorizationRole_ADMIN}))
	require.NoError(t, err)
	_, err = handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: target.MemberID}))

	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	identity, err := stack.KratosClient.GetIdentity(t.Context(), target.IdentityID)
	require.NoError(t, err)
	require.Equal(t, auth.KratosStateActive, identity.State)
	require.False(t, identity.IsBanned())
	requireGlobalRolePermission(t, stack.SpiceDBClient, target.IdentityID, policyv1.Platform.IsAdmin, true)
}

// Two administrators demoting each other at once: the identity locks and the
// last-admin fence let exactly one through.
func TestSetAccountRoleConcurrentPeerDemotionKeepsOneActiveAdminIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	first := stack.CreateUser(t, policyv1.Role.Admin().ID())
	second := stack.CreateUser(t, policyv1.Role.Admin().ID())
	service, _, _ := integrationService(t, stack)
	handler := account.NewConnectHandler(service)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, pair := range [][2]*orytest.OryUser{{first, second}, {second, first}} {
		go func() {
			<-start
			_, err := handler.SetAccountRole(auditedOryMemberContext(t, pair[0]), connect.NewRequest(&managev1.SetAccountRoleRequest{
				MemberId: pair[1].MemberID, Role: policyv1.AuthorizationRole_USER,
			}))
			results <- err
		}()
	}
	close(start)

	var successes, denied int
	for range 2 {
		result := <-results
		switch {
		case result == nil:
			successes++
		case connect.CodeOf(result) == connect.CodePermissionDenied:
			denied++
		default:
			t.Fatalf("unexpected concurrent role mutation result: %v", result)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, denied)
	var auditCount int64
	require.NoError(t, stack.DB.Table("public.domain_audit").
		Where("action = ? AND target_type = ? AND target_id IN (?, ?)", sharedtelemetry.AuditMemberUpdated, "member", first.MemberID, second.MemberID).
		Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
	activeAdmins := 0
	can, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	for _, user := range []*orytest.OryUser{first, second} {
		actor, err := policyv1.NewAccountIdentityActor(user.IdentityID)
		require.NoError(t, err)
		admin, err := stack.SpiceDBClient.CheckActorCan(t.Context(), actor, can)
		require.NoError(t, err)
		if admin {
			activeAdmins++
		}
	}
	require.Equal(t, 1, activeAdmins)
}

// The retention scheduler and the deletion worker remove an unonboarded
// Member across Kratos, SpiceDB and PostgreSQL once; a redelivery changes
// nothing.
func TestUnonboardedDeletionWorkerHardDeletesOnceAndCleansSpiceDBIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	now := time.Now().UTC()
	require.NoError(t, stack.DB.Exec(
		"UPDATE member SET onboarded = FALSE, created_at = ?, updated_at = ? WHERE id = ?::uuid",
		now.Add(-account.UnonboardedMemberRetention-time.Hour), now, target.MemberID,
	).Error)
	policy, err := policyv1.Member.TouchPolicy(target.MemberID)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	requireMemberView(t, stack.SpiceDBClient, target.MemberID, admin.IdentityID, true)
	_, units, _ := integrationService(t, stack)
	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, pgtest.PurgePGMQQueue(context.Background(), sqlDB, eventpkg.QueueUserDeleteIdentity))
	})

	queued, err := account.EnqueueExpiredUnonboardedMembers(t.Context(), units, now, 1)
	require.NoError(t, err)
	require.Equal(t, 1, queued)
	emails := &accounttest.Emails{}
	deletion := account.UserDeletion{
		Units: units, Identities: stack.KratosClient, SpiceDB: stack.SpiceDBClient,
		Members: accountpostgres.MemberAuthorizations(stack.DB, accountadapter.MemberDeletion{}, stack.SpiceDBClient),
		Emails:  emails, Audit: apitelemetry.NewDurableWriter(stack.DB),
	}
	command := account.DeleteIdentityCommand{Mode: account.DeleteIdentityUnonboardedHardDelete, MemberID: target.MemberID, IdentityID: target.IdentityID}
	require.NoError(t, deletion.ProcessIdentity(t.Context(), command))
	require.NoError(t, deletion.ProcessIdentity(t.Context(), command))

	var memberCount, identityCount, anchorCount, terminalAuditCount int64
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM member WHERE id = ?::uuid", target.MemberID).Scan(&memberCount).Error)
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM kratos.identities WHERE id = ?::uuid", target.IdentityID).Scan(&identityCount).Error)
	require.NoError(t, stack.DB.Raw("SELECT COUNT(*) FROM account_identity WHERE id = ?::uuid", target.IdentityID).Scan(&anchorCount).Error)
	require.NoError(t, stack.DB.Raw(`
		SELECT COUNT(*) FROM public.domain_audit
		WHERE action = ? AND target_type = 'account' AND target_id = ? AND actor_kind = 'system' AND actor_service = ?
	`, sharedtelemetry.AuditAccountDeleted, target.MemberID, sharedtelemetry.ServiceBackend).Scan(&terminalAuditCount).Error)
	require.Zero(t, memberCount)
	require.Zero(t, identityCount)
	require.Zero(t, anchorCount)
	require.Equal(t, int64(1), terminalAuditCount)
	require.Empty(t, emails.Sent())
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(target.IdentityID))
	require.NoError(t, err)
	_, hasRole, err := stack.SpiceDBClient.ReadDirectGlobalRole(t.Context(), subject)
	require.NoError(t, err)
	require.False(t, hasRole)
	requireMemberView(t, stack.SpiceDBClient, target.MemberID, admin.IdentityID, false)
}

func requireMemberView(t *testing.T, spiceDB *auth.SpiceDBClient, memberID, identityID string, want bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := policyv1.Member.View(memberID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed)
}
