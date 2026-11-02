package member_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/membertest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type fakeRegistrationEmails struct {
	email  string
	synced []string
}

func (f *fakeRegistrationEmails) PrepareRegistration(ctx context.Context, identities auth.IdentityGetter, identityID, _ string) (*auth.Identity, string, []string, error) {
	identity, err := identities.GetIdentity(ctx, identityID)
	return identity, f.email, []string{f.email}, err
}

func (f *fakeRegistrationEmails) SyncMemberEmailProjection(_ context.Context, identityID string, _ *auth.Identity) error {
	f.synced = append(f.synced, identityID)
	return nil
}

type fakeRoleTransition struct{}

func (fakeRoleTransition) Transition(subject auth.AccountIdentitySubject, desired, _ policyv1.RoleID, _ bool) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, nil, err
	}
	touch, err := policyv1.Role.TouchMember(desired, actor)
	if err != nil {
		return nil, nil, err
	}
	remove, err := policyv1.Role.DeleteMember(desired, actor)
	return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, err
}

type provisioningFixture struct {
	store       *membertest.Store
	units       *mutationtest.UnitOfWork[member.Store]
	checker     *authztest.Checker
	identity    *fakeIdentity
	emails      *fakeRegistrationEmails
	provisioner *member.MemberProvisioner
	identityID  string
}

func newProvisioningFixture(t *testing.T) *provisioningFixture {
	t.Helper()
	store, units := membertest.New()
	f := &provisioningFixture{
		store: store, units: units, checker: authztest.AllowAll(),
		identity: &fakeIdentity{identities: map[string]*auth.Identity{}}, emails: &fakeRegistrationEmails{email: "new@example.test"},
		identityID: uuid.NewString(),
	}
	f.identity.put(&auth.Identity{ID: f.identityID, State: auth.KratosStateActive})
	store.SeedIdentity(f.identityID, membertest.Identity{})
	f.provisioner = member.NewMemberProvisioner(units, f.identity, membertest.NewAuthorizer(f.checker), f.emails, fakeRoleTransition{})
	return f
}

func (f *provisioningFixture) input() member.RegistrationMemberInput {
	return member.RegistrationMemberInput{IdentityID: f.identityID, Email: "new@example.test", PreferredLocale: "ko"}
}

func TestProvisionRegistrationCreatesTheLinkedMemberAndItsRelationships(t *testing.T) {
	t.Parallel()
	f := newProvisioningFixture(t)

	provisioned, err := f.provisioner.ProvisionRegistration(t.Context(), f.input())
	require.NoError(t, err)
	require.False(t, provisioned.Onboarded)
	require.Equal(t, provisioned.ID, provisioned.Nickname)
	require.Equal(t, &f.identityID, provisioned.AccountIdentityID)
	require.Equal(t, []string{"new@example.test"}, provisioned.AvailableEmails)
	require.Equal(t, provisioned.ID, f.identity.externalID[f.identityID])
	require.Equal(t, []string{f.identityID}, f.store.Anchors())
	require.Equal(t, []string{f.identityID}, f.emails.synced)
	applied := f.checker.Applied()
	require.Len(t, applied, 1)
	memberPolicy, err := policyv1.Member.TouchPolicy(provisioned.ID)
	require.NoError(t, err)
	require.Equal(t, memberPolicy, applied[0][0])
	require.Len(t, applied[0], 2)

	f.identity.put(&auth.Identity{ID: f.identityID, ExternalID: provisioned.ID, State: auth.KratosStateActive})
	retried, err := f.provisioner.ProvisionRegistration(t.Context(), f.input())
	require.NoError(t, err)
	require.Equal(t, provisioned.ID, retried.ID)
	require.Len(t, f.checker.Applied(), 1)
}

func TestProvisionRegistrationRejectsAConflictingExternalIDBeforeMemberCreate(t *testing.T) {
	t.Parallel()
	f := newProvisioningFixture(t)
	f.identity.put(&auth.Identity{ID: f.identityID, ExternalID: uuid.NewString(), State: auth.KratosStateActive})

	_, err := f.provisioner.ProvisionRegistration(t.Context(), f.input())

	require.ErrorContains(t, err, "identity.external_id conflicts with the member reverse pointer")
	members, listErr := f.store.Members(t.Context(), nil)
	require.NoError(t, listErr)
	require.Empty(t, members)
	require.Empty(t, f.checker.Applied())
}

func TestProvisionRegistrationEnforcesTheDeletedMemberEmailReuseHold(t *testing.T) {
	t.Parallel()
	f := newProvisioningFixture(t)
	deletedAt := time.Now().UTC().Add(-time.Hour)
	f.store.SeedMember(member.Member{ID: uuid.NewString(), Nickname: "former", AvailableEmails: []string{"new@example.test"}, DeletedAt: &deletedAt})

	_, err := f.provisioner.ProvisionRegistration(t.Context(), f.input())
	require.ErrorContains(t, err, "registration cannot be completed")

	_, err = f.provisioner.ProvisionRegistration(t.Context(), member.RegistrationMemberInput{IdentityID: f.identityID, Email: "invalid", PreferredLocale: "ko"})
	require.ErrorContains(t, err, "registration email is invalid")
	_, err = f.provisioner.ProvisionRegistration(t.Context(), member.RegistrationMemberInput{IdentityID: f.identityID, Email: "new@example.test", PreferredLocale: "xx"})
	require.ErrorContains(t, err, "preferred_locale must be a supported locale")
}

func TestValidateExistingLinkSyncsTheEmailProjection(t *testing.T) {
	t.Parallel()
	f := newProvisioningFixture(t)
	_, err := f.provisioner.ValidateExistingLink(t.Context(), &auth.Identity{ID: f.identityID})
	require.ErrorIs(t, err, member.ErrMemberLinkRepairable)

	memberID := uuid.NewString()
	f.store.SeedLinked(memberID, f.identityID, "linked")
	linked, err := f.provisioner.ValidateExistingLink(t.Context(), &auth.Identity{ID: f.identityID, ExternalID: memberID})
	require.NoError(t, err)
	require.Equal(t, memberID, linked.ID)
	require.Equal(t, []string{f.identityID}, f.emails.synced)

	_, err = f.provisioner.ValidateExistingLink(t.Context(), &auth.Identity{ID: f.identityID, ExternalID: uuid.NewString()})
	require.ErrorContains(t, err, "validate existing identity/member link")
}
