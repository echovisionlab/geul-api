package authentication_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/authentication/authenticationtest"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// loginRoles is the login role sync of one test on fakes.
type loginRoles struct {
	service *authentication.Service
	store   *authenticationtest.Store
	units   *mutationtest.UnitOfWork[authentication.Store]
	spiceDB *authenticationtest.Authorizer
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
}

func newLoginRoles(t *testing.T, audit *audittest.Recorder) loginRoles {
	t.Helper()
	store, units := authenticationtest.New()
	roles := loginRoles{
		store: store, units: units, audit: audit,
		spiceDB: authenticationtest.NewAuthorizer(&authztest.Checker{}), fence: &identitystatetest.Fence{},
	}
	var err error
	roles.service, err = authentication.NewService(authentication.Deps{
		Units: units, SpiceDB: roles.spiceDB, Fence: roles.fence, Audit: audit,
		Roles: accountadapter.AccountDirectRoleTransition{}, Sessions: authenticationtest.NewSessions(),
	})
	require.NoError(t, err)
	return roles
}

// seedMember stores an onboarded Member linked to a new Identity.
func (r loginRoles) seedMember() (identityID, memberID string) {
	identityID, memberID = uuid.NewString(), uuid.NewString()
	r.store.SeedMember(memberID, authenticationtest.Member{IdentityID: identityID, Onboarded: true})
	return identityID, memberID
}

func replacement(t *testing.T, identityID string, role policyv1.RoleID) []policyv1.RelationshipMutation {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	apply, err := account.RoleReplacementMutations(subject, role)
	require.NoError(t, err)
	return apply
}

var (
	committedRoleWrite = []mutation.Step{mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRelationships, mutation.StepCommit}
	unchangedRole      = []mutation.Step{mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepRollback}
)

func TestNewServiceRejectsMissingRequiredDependencies(t *testing.T) {
	_, err := authentication.NewService(authentication.Deps{SpiceDB: authenticationtest.NewAuthorizer(&authztest.Checker{})})
	require.EqualError(t, err, "authentication dependencies are required: Audit, Fence, Roles, Sessions, Units")
	_, units := authenticationtest.New()
	_, err = authentication.NewService(authentication.Deps{Units: units, Sessions: authenticationtest.NewSessions()})
	require.EqualError(t, err, "authentication dependencies are required: Audit, Fence, Roles, SpiceDB")
}

func TestFirstLoginOfTheGreenfieldClaimsTheFirstAdminAsTheBackend(t *testing.T) {
	roles := newLoginRoles(t, &audittest.Recorder{})
	identityID, memberID := roles.seedMember()

	result, err := roles.service.SyncLoginRole(t.Context(), identityID, memberID)

	require.NoError(t, err)
	require.Equal(t, authentication.LoginRoleSyncResult{Role: policyv1.Role.Admin(), FirstAdmin: true}, result)
	claimedBy, claimedFor, claimed := roles.store.FirstAdmin()
	require.True(t, claimed)
	require.Equal(t, []string{identityID, memberID}, []string{claimedBy, claimedFor})
	require.Equal(t, [][]policyv1.RelationshipMutation{replacement(t, identityID, policyv1.Role.Admin())}, roles.spiceDB.Applied())
	require.Equal(t, []string{identityID}, roles.fence.Locked())
	require.Equal(t, []string{identityID}, roles.spiceDB.Reads())
	require.Equal(t, committedRoleWrite, roles.units.Steps())
	records := roles.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditMemberUpdated, records[0].Action)
	require.Equal(t, sharedtelemetry.ActorKindSystem, records[0].Kind)
	require.Equal(t, string(sharedtelemetry.ServiceBackend), records[0].Service)
	require.Equal(t, memberID, records[0].TargetID)
	require.Equal(t, policyv1.Role.User().ID(), records[0].PreviousRole)
	require.Equal(t, policyv1.Role.Admin().ID(), records[0].NewRole)

	again, err := roles.service.EnsureLoginRole(t.Context(), identityID, memberID)
	require.NoError(t, err)
	require.False(t, again, "only the claiming login reports the first admin")
}

func TestLoginWithoutADirectRoleBecomesAUserWithoutAudit(t *testing.T) {
	roles := newLoginRoles(t, &audittest.Recorder{})
	roles.store.SeedIdentity(uuid.NewString())
	identityID, memberID := roles.seedMember()

	result, err := roles.service.SyncLoginRole(t.Context(), identityID, memberID)

	require.NoError(t, err)
	require.Equal(t, authentication.LoginRoleSyncResult{Role: policyv1.Role.User()}, result)
	require.Equal(t, [][]policyv1.RelationshipMutation{replacement(t, identityID, policyv1.Role.User())}, roles.spiceDB.Applied())
	require.Empty(t, roles.audit.Records())
	_, _, claimed := roles.store.FirstAdmin()
	require.False(t, claimed)
	require.Equal(t, committedRoleWrite, roles.units.Steps())
}

func TestLoginKeepsAnExistingDirectRoleWithoutWriting(t *testing.T) {
	for _, role := range []policyv1.RoleID{policyv1.Role.User(), policyv1.Role.Author(), policyv1.Role.Admin()} {
		t.Run(role.ID(), func(t *testing.T) {
			roles := newLoginRoles(t, &audittest.Recorder{})
			roles.store.SeedIdentity(uuid.NewString())
			identityID, memberID := roles.seedMember()
			roles.spiceDB.SetRole(identityID, role)

			result, err := roles.service.SyncLoginRole(t.Context(), identityID, memberID)

			require.NoError(t, err)
			require.Equal(t, authentication.LoginRoleSyncResult{Role: role}, result)
			require.Empty(t, roles.spiceDB.Applied())
			require.Empty(t, roles.audit.Records())
			require.Equal(t, unchangedRole, roles.units.Steps(), "an unchanged role rolls its transaction back")
		})
	}
}

func TestLoginRepairsTheFirstAdminOwnerWithoutAuditingAgain(t *testing.T) {
	roles := newLoginRoles(t, &audittest.Recorder{})
	identityID, memberID := roles.seedMember()
	_, err := roles.service.SyncLoginRole(t.Context(), identityID, memberID)
	require.NoError(t, err)
	roles.spiceDB.SetRole(identityID, policyv1.Role.User())

	result, err := roles.service.SyncLoginRole(t.Context(), identityID, memberID)

	require.NoError(t, err)
	require.Equal(t, authentication.LoginRoleSyncResult{Role: policyv1.Role.Admin()}, result)
	require.Equal(t, [][]policyv1.RelationshipMutation{
		replacement(t, identityID, policyv1.Role.Admin()), replacement(t, identityID, policyv1.Role.Admin()),
	}, roles.spiceDB.Applied())
	require.Len(t, roles.audit.Records(), 1, "only the claim is audited")
}

func TestFailedFirstAdminAuditLeavesNeitherClaimNorRole(t *testing.T) {
	auditDown := errors.New("bootstrap audit unavailable")
	roles := newLoginRoles(t, &audittest.Recorder{Err: auditDown})
	identityID, memberID := roles.seedMember()

	result, err := roles.service.SyncLoginRole(t.Context(), identityID, memberID)

	require.ErrorIs(t, err, auditDown)
	require.Empty(t, result)
	_, _, claimed := roles.store.FirstAdmin()
	require.False(t, claimed)
	require.Empty(t, roles.spiceDB.Applied(), "the audit precedes the relationship write")
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRollback}, roles.units.Steps())
}

func TestFailedRoleWriteRollsTheClaimBack(t *testing.T) {
	writeDown := errors.New("SpiceDB unavailable")
	roles := newLoginRoles(t, &audittest.Recorder{})
	roles.spiceDB.FailWrites(writeDown)
	identityID, memberID := roles.seedMember()

	_, err := roles.service.SyncLoginRole(t.Context(), identityID, memberID)

	require.ErrorIs(t, err, writeDown)
	_, _, claimed := roles.store.FirstAdmin()
	require.False(t, claimed)
	require.Equal(t, mutation.StepRollback, roles.units.Steps()[len(roles.units.Steps())-1])
}

func TestFailedRoleReadClaimsNothing(t *testing.T) {
	readDown := errors.New("SpiceDB unavailable")
	roles := newLoginRoles(t, &audittest.Recorder{})
	roles.spiceDB.FailReads(readDown)
	identityID, memberID := roles.seedMember()

	_, err := roles.service.EnsureLoginRole(t.Context(), identityID, memberID)

	require.ErrorIs(t, err, readDown)
	require.ErrorContains(t, err, "read login direct global role")
	_, _, claimed := roles.store.FirstAdmin()
	require.False(t, claimed)
	require.Empty(t, roles.spiceDB.Applied())
}

func TestLoginRoleSyncRejectsANonCanonicalIdentityBeforeItsTransaction(t *testing.T) {
	roles := newLoginRoles(t, &audittest.Recorder{})

	_, err := roles.service.SyncLoginRole(t.Context(), "not-an-identity", uuid.NewString())

	require.Error(t, err)
	require.Empty(t, roles.units.Steps())
	require.Empty(t, roles.spiceDB.Reads())
}
