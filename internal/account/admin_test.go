package account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// grantRole makes identityID hold role in the checker, and every role the
// schema derives from it, and records it as the direct role.
func grantRole(f fixture, checker *authztest.Checker, identityID string, role policyv1.RoleID) {
	holds := map[policyv1.RoleID][]func() (policyv1.Can, error){
		policyv1.Role.Admin():  {policyv1.Platform.IsAdmin, policyv1.Platform.IsAuthor, policyv1.Platform.IsUser},
		policyv1.Role.Author(): {policyv1.Platform.IsAuthor, policyv1.Platform.IsUser},
		policyv1.Role.User():   {policyv1.Platform.IsUser},
	}
	for _, can := range holds[role] {
		checker.For(identityID).Allow(can())
	}
	f.authorizer.Roles[identityID] = role
	lookups := map[policyv1.RoleID]policyv1.SubjectLookup{
		policyv1.Role.Admin():  policyv1.Platform.LookupAdminSubjects(),
		policyv1.Role.Author(): policyv1.Platform.LookupAuthorSubjects(),
		policyv1.Role.User():   policyv1.Platform.LookupUserSubjects(),
	}
	key := lookups[role].Permission()
	f.authorizer.Subjects[key] = append(f.authorizer.Subjects[key], identityID)
}

type adminFixture struct {
	fixture
	checker                  *authztest.Checker
	handler                  *account.ConnectHandler
	actorID, actorIdentityID string
	memberID, identityID     string
}

// newAdminFixture seeds an administrator and a target Member that holds
// targetRole.
func newAdminFixture(t *testing.T, targetRole policyv1.RoleID) adminFixture {
	t.Helper()
	checker := &authztest.Checker{}
	f := newFixture(t, checker)
	a := adminFixture{fixture: f, checker: checker, handler: account.NewConnectHandler(f.service)}
	a.actorID, a.actorIdentityID = f.seedPair("")
	a.memberID, a.identityID = f.seedPair("")
	grantRole(f, checker, a.actorIdentityID, policyv1.Role.Admin())
	grantRole(f, checker, a.identityID, targetRole)
	return a
}

func (a adminFixture) ctx(t *testing.T) context.Context {
	return principalContext(t, a.actorID, a.actorIdentityID)
}

func TestSetAccountRoleRejectsSelfBeforeMutation(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())

	response, err := a.handler.SetAccountRole(a.ctx(t), connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: a.actorID, Role: policyv1.AuthorizationRole_USER,
	}))

	require.Nil(t, response)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "cannot change your own role")
	require.Empty(t, a.units.Steps())
}

func TestSetAccountRoleWritesTheDirectRoleAndItsAudit(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())

	_, err := a.handler.SetAccountRole(a.ctx(t), connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: a.memberID, Role: policyv1.AuthorizationRole_AUTHOR,
	}))
	require.NoError(t, err)

	require.Len(t, a.checker.Applied(), 1)
	records := a.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditMemberUpdated, records[0].Action)
	require.Equal(t, a.memberID, records[0].TargetID)
	require.Equal(t, []string{"role"}, records[0].ChangedFields)
	require.Equal(t, policyv1.Role.User().ID(), records[0].PreviousRole)
	require.Equal(t, policyv1.Role.Author().ID(), records[0].NewRole)
	require.Equal(t, sharedtelemetry.ActorKindMember, records[0].Kind)
	require.Equal(t, a.actorID, records[0].MemberID)
	steps := a.units.Steps()
	require.Equal(t, mutation.StepCommit, steps[len(steps)-1])
}

func TestSetAccountRoleSameRoleDoesNotWriteAudit(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.Author())

	_, err := a.handler.SetAccountRole(a.ctx(t), connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: a.memberID, Role: policyv1.AuthorizationRole_AUTHOR,
	}))
	require.NoError(t, err)

	require.Empty(t, a.checker.Applied())
	require.Empty(t, a.audit.Records())
	require.Contains(t, a.units.Steps(), mutation.StepRollback)
}

// The audit record is appended before the SpiceDB batch, so a failing audit
// stops the role change before it reaches SpiceDB and leaves nothing to
// compensate.
func TestSetAccountRoleAuditFailureStopsBeforeSpiceDB(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())
	a.audit.Err = errors.New("audit unavailable")

	response, err := a.handler.SetAccountRole(a.ctx(t), connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: a.memberID, Role: policyv1.AuthorizationRole_AUTHOR,
	}))

	require.Nil(t, response)
	require.ErrorContains(t, err, "audit unavailable")
	require.NotContains(t, err.Error(), "compensate authorization relationships")
	require.Empty(t, a.checker.Applied())
	require.Empty(t, a.checker.Compensated())
}

func TestSetAccountRoleRevalidatesActorLifecycleUnderIdentityFences(t *testing.T) {
	t.Parallel()
	for name, invalidate := range map[string]func(a adminFixture){
		"banned": func(a adminFixture) {
			a.store.UpdateIdentity(a.actorIdentityID, func(identity *accounttest.Identity) { identity.Banned = true })
		},
		"inactive": func(a adminFixture) {
			a.store.UpdateIdentity(a.actorIdentityID, func(identity *accounttest.Identity) { identity.State = auth.KratosStateInactive })
		},
		"pending_deletion": func(a adminFixture) {
			now := time.Now().UTC()
			a.store.SeedDeletionRequest(account.DeletionRequest{
				ID: uuid.NewString(), MemberID: a.actorID, IdentityID: a.actorIdentityID, Token: uuid.NewString(),
				TokenExpiresAt: now.Add(time.Hour), LifecycleState: "scheduled", ScheduledAt: &now, CreatedAt: now, UpdatedAt: now,
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newAdminFixture(t, policyv1.Role.User())
			invalidate(a)

			response, err := a.handler.SetAccountRole(a.ctx(t), connect.NewRequest(&managev1.SetAccountRoleRequest{
				MemberId: a.memberID, Role: policyv1.AuthorizationRole_AUTHOR,
			}))

			require.Nil(t, response)
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			require.Empty(t, a.checker.Applied())
			require.Empty(t, a.audit.Records())
		})
	}
}

// An administrator who is not onboarded does not count as the remaining
// active administrator, so the last onboarded one cannot be demoted.
func TestSetAccountRoleKeepsUnonboardedMemberOutOfLastAdminCount(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.Admin())
	a.store.UpdateMember(a.actorID, func(member *accounttest.Member) { member.Onboarded = false })

	_, err := a.service.SetAccountRole(a.ctx(t), account.SetAccountRoleCommand{MemberID: a.memberID, Role: policyv1.Role.User()})

	require.ErrorIs(t, err, account.ErrLastActiveAdminDeletion)
	require.Empty(t, a.checker.Applied())
	require.Empty(t, a.audit.Records())
}

// Administrator mutations of an unonboarded Member leave it unonboarded.
func TestAccountAdminMutationsKeepUnonboardedTargetUnonboarded(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())
	a.store.UpdateMember(a.memberID, func(member *accounttest.Member) { member.Onboarded = false })
	ctx := a.ctx(t)

	_, err := a.handler.SetAccountRole(ctx, connect.NewRequest(&managev1.SetAccountRoleRequest{
		MemberId: a.memberID, Role: policyv1.AuthorizationRole_AUTHOR,
	}))
	require.NoError(t, err)
	require.Len(t, a.checker.Applied(), 1)
	_, err = a.handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: a.memberID}))
	require.NoError(t, err)
	_, err = a.handler.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: a.memberID}))
	require.NoError(t, err)
	_, err = a.handler.RemoveAccountSsoProvider(ctx, connect.NewRequest(&managev1.RemoveAccountSsoProviderRequest{
		MemberId: a.memberID, Provider: "google", Identifier: "missing-provider-subject",
	}))
	require.NoError(t, err)

	member, found := a.store.Member(a.memberID)
	require.True(t, found)
	require.False(t, member.Onboarded)
	records := a.audit.Records()
	require.Len(t, records, 3)
	require.Equal(t, policyv1.Role.Author().ID(), records[0].NewRole)
}

func TestEnsureAccountBanTargetIsNonAdminUsesIdentityRoleAuthority(t *testing.T) {
	t.Parallel()
	t.Run("ordinary identity is bannable", func(t *testing.T) {
		t.Parallel()
		a := newAdminFixture(t, policyv1.Role.Author())
		_, err := a.handler.BanAccount(a.ctx(t), connect.NewRequest(&managev1.BanAccountRequest{MemberId: a.memberID}))
		require.NoError(t, err)
		require.True(t, a.kratos.Identity(a.identityID).IsBanned())
	})
	t.Run("admin identity is never bannable", func(t *testing.T) {
		t.Parallel()
		a := newAdminFixture(t, policyv1.Role.Admin())
		_, err := a.handler.BanAccount(a.ctx(t), connect.NewRequest(&managev1.BanAccountRequest{MemberId: a.memberID}))
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		require.ErrorContains(t, err, "admin accounts cannot be banned")
		require.Empty(t, a.kratos.Writes())
		require.Empty(t, a.audit.Records())
	})
	t.Run("identity lookup failure is fail closed", func(t *testing.T) {
		t.Parallel()
		a := newAdminFixture(t, policyv1.Role.Author())
		a.kratos.FailIdentity(a.identityID, errors.New("identity unavailable"))
		_, err := a.handler.BanAccount(a.ctx(t), connect.NewRequest(&managev1.BanAccountRequest{MemberId: a.memberID}))
		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		require.Empty(t, a.kratos.Writes())
	})
	t.Run("missing identity is fail closed", func(t *testing.T) {
		t.Parallel()
		a := newAdminFixture(t, policyv1.Role.Author())
		service, err := account.NewService(account.Deps{
			Units: a.units, SpiceDB: a.authorizer, Fence: a.fence, Identity: lookupFailure{Kratos: a.kratos},
			Emails: a.emails, Members: a.members, Audit: a.audit,
		})
		require.NoError(t, err)
		_, err = service.BanAccount(a.ctx(t), account.BanAccountCommand{MemberID: a.memberID})
		require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
		require.Empty(t, a.kratos.Writes())
	})
}

// Banning and unbanning record only real transitions: a repeated request
// changes nothing and records nothing.
func TestAccountBanAuditRecordsOnlyExactTransitions(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())
	ctx := a.ctx(t)
	for range 2 {
		_, err := a.handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: a.memberID}))
		require.NoError(t, err)
	}
	for range 2 {
		_, err := a.handler.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: a.memberID}))
		require.NoError(t, err)
	}

	records := a.audit.Records()
	require.Len(t, records, 2)
	for index, transition := range [][2]sharedtelemetry.AuditState{
		{sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateBanned},
		{sharedtelemetry.AuditStateBanned, sharedtelemetry.AuditStateActive},
	} {
		require.Equal(t, sharedtelemetry.AuditMemberUpdated, records[index].Action)
		require.Equal(t, []string{"status"}, records[index].ChangedFields)
		require.Equal(t, transition[0], records[index].PreviousState)
		require.Equal(t, transition[1], records[index].NewState)
		require.Empty(t, records[index].PreviousRole)
		require.Equal(t, sharedtelemetry.ActorKindMember, records[index].Kind)
		require.Equal(t, a.actorID, records[index].MemberID)
		require.Empty(t, records[index].Service)
	}
	identity := a.kratos.Identity(a.identityID)
	require.False(t, identity.IsBanned())
	require.Equal(t, auth.KratosStateActive, identity.State)
}

func TestUnbanAccountRejectsPendingDeletion(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())
	_, err := a.handler.BanAccount(a.ctx(t), connect.NewRequest(&managev1.BanAccountRequest{MemberId: a.memberID}))
	require.NoError(t, err)
	now := time.Now().UTC()
	a.store.SeedDeletionRequest(account.DeletionRequest{
		ID: uuid.NewString(), MemberID: a.memberID, IdentityID: a.identityID, Token: uuid.NewString(),
		TokenExpiresAt: now.Add(time.Hour), LifecycleState: "scheduled", ScheduledAt: &now, CreatedAt: now, UpdatedAt: now,
	})
	writes := len(a.kratos.Writes())

	_, err = a.handler.UnbanAccount(a.ctx(t), connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: a.memberID}))

	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.ErrorContains(t, err, "pending deletion cannot be unbanned")
	require.Len(t, a.kratos.Writes(), writes)
	require.True(t, a.kratos.Identity(a.identityID).IsBanned())
	require.Len(t, a.audit.Records(), 1)
}

// An administrator whose Identity the fence finds inactive changes nothing,
// although its SpiceDB role still answers.
func TestAccountAdminMutationsRejectStaleAdministratorIdentity(t *testing.T) {
	t.Parallel()
	a := newAdminFixture(t, policyv1.Role.User())
	a.fence.Deactivate(a.actorIdentityID)
	ctx := a.ctx(t)

	for name, call := range map[string]func() error{
		"ban": func() error {
			_, err := a.handler.BanAccount(ctx, connect.NewRequest(&managev1.BanAccountRequest{MemberId: a.memberID}))
			return err
		},
		"unban": func() error {
			_, err := a.handler.UnbanAccount(ctx, connect.NewRequest(&managev1.UnbanAccountRequest{MemberId: a.memberID}))
			return err
		},
		"canonical email": func() error {
			_, err := a.handler.SetAccountCanonicalEmail(ctx, connect.NewRequest(&managev1.SetAccountCanonicalEmailRequest{MemberId: a.memberID, Email: "next@example.test"}))
			return err
		},
		"sso provider": func() error {
			_, err := a.handler.RemoveAccountSsoProvider(ctx, connect.NewRequest(&managev1.RemoveAccountSsoProviderRequest{MemberId: a.memberID, Provider: "google", Identifier: "subject"}))
			return err
		},
		"newsletter": func() error {
			_, err := a.handler.UnsubscribeAccountFromNewsletter(ctx, connect.NewRequest(&managev1.UnsubscribeAccountFromNewsletterRequest{MemberId: a.memberID}))
			return err
		},
		"delete": func() error {
			_, err := a.handler.DeleteAccount(ctx, connect.NewRequest(&managev1.DeleteAccountRequest{MemberId: a.memberID}))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			require.Equal(t, "admin access required", connectErr.Message())
		})
	}
	require.Empty(t, a.kratos.Writes())
	require.Empty(t, a.store.DeletionRequests())
	require.Empty(t, a.store.IdentityCommands())
	require.Empty(t, a.audit.Records())
}
