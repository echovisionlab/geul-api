//go:build integration

package member

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type concurrentProvisioningIdentity struct {
	mu       sync.Mutex
	db       *gorm.DB
	identity auth.Identity
}

func (m *concurrentProvisioningIdentity) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := m.identity
	copy.Traits = map[string]interface{}{}
	for key, value := range m.identity.Traits {
		copy.Traits[key] = value
	}
	return &copy, nil
}

func (m *concurrentProvisioningIdentity) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return m.GetIdentity(ctx, identityID)
}

func (m *concurrentProvisioningIdentity) UpdateIdentityExternalID(ctx context.Context, identityID, memberID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.identity.ID != identityID {
		return fmt.Errorf("unexpected identity id %q", identityID)
	}
	if m.identity.ExternalID != "" && m.identity.ExternalID != memberID {
		return context.Canceled
	}
	result := m.db.WithContext(ctx).Exec(
		`UPDATE kratos.identities SET external_id = ?::text WHERE id = ?::uuid`, memberID, identityID,
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("identity %s was not found", identityID)
	}
	m.identity.ExternalID = memberID
	return nil
}

func TestProvisionRegistrationConcurrentHooksConvergeOnOneMember(t *testing.T) {
	stack := newConcurrentServiceIntegrationPostgres(t)
	oryStack := testutil.SetupOryStack(t)
	spiceDB := oryStack.SpiceDBClient
	admin := oryStack.CreateUser(t, policyv1.Role.Admin().ID())
	identityID := uuid.NewString()
	require.NoError(t, stack.DB.Exec(`
		INSERT INTO kratos.identities (id, external_id, schema_id, traits, state, created_at, updated_at)
		VALUES (?::uuid, NULL, 'user', jsonb_build_object('email', 'member@example.test'), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, identityID).Error)

	identity := &concurrentProvisioningIdentity{db: stack.DB, identity: auth.Identity{
		ID:       identityID,
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits:   map[string]interface{}{"email": "member@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Via: "email", Value: "member@example.test", Verified: true,
		}},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"member@example.test"}},
		},
		ExternalID: "",
	}}
	provisioner := integrationMemberProvisioner(stack.DB, identity, spiceDB)
	input := RegistrationMemberInput{
		IdentityID:      identityID,
		Email:           "member@example.test",
		PreferredLocale: "ko-KR",
	}

	const workers = 8
	memberIDs := make(chan string, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			member, err := provisioner.ProvisionRegistration(t.Context(), input)
			if err != nil {
				errors <- err
				return
			}
			memberIDs <- member.ID
		}()
	}
	group.Wait()
	close(errors)
	close(memberIDs)
	for err := range errors {
		require.NoError(t, err)
	}
	var expected string
	for memberID := range memberIDs {
		if expected == "" {
			expected = memberID
		}
		require.Equal(t, expected, memberID)
	}
	require.NotEmpty(t, expected)

	var count int64
	require.NoError(t, stack.DB.Model(&model.Member{}).
		Where("account_identity_id = ?", identityID).
		Count(&count).Error)
	require.Equal(t, int64(1), count)
	var provisioned model.Member
	require.NoError(t, stack.DB.Where("id = ?::uuid", expected).Take(&provisioned).Error)
	require.Equal(t, provisioned.ID, provisioned.Nickname)
	require.False(t, provisioned.Onboarded)
	require.NotNil(t, provisioned.PreferredLocale)
	require.Equal(t, "ko", *provisioned.PreferredLocale)
	subject := requireAccountIdentitySubject(t, identityID)
	role, found, err := spiceDB.ReadDirectGlobalRole(t.Context(), subject)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, policyv1.Role.User(), role)
	memberActor, err := policyv1.NewAccountIdentityActor(admin.IdentityID)
	require.NoError(t, err)
	memberViewCan, err := policyv1.Member.View(expected)
	require.NoError(t, err)
	memberView, err := spiceDB.CheckActorCan(t.Context(), memberActor, memberViewCan)
	require.NoError(t, err)
	require.True(t, memberView)
}

func TestAllocateMemberUUIDSkipsExistingUUIDNickname(t *testing.T) {
	db := newServiceIntegrationDB(t)
	claimedUUIDNickname := uuid.NewString()
	availableUUID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, nickname, onboarded, available_emails)
		VALUES (?::uuid, ?, TRUE, '{}'::text[])
	`, uuid.NewString(), claimedUUIDNickname).Error)

	candidates := []string{claimedUUIDNickname, availableUUID}
	index := 0
	allocated, err := allocateMemberUUID(db, func() string {
		candidate := candidates[index]
		index++
		return candidate
	})
	require.NoError(t, err)
	require.Equal(t, availableUUID, allocated)
	require.Equal(t, 2, index)
}

func TestProvisionRegistrationIgnoresNonCanonicalEmailCodeIdentifiers(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	const currentEmail = "z-current@example.test"
	const secondaryEmail = "a-secondary@example.test"
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (id, external_id, schema_id, traits, state, created_at, updated_at)
		VALUES (?::uuid, NULL, 'user', jsonb_build_object('email', ?::text), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, identityID, currentEmail).Error)

	identity := &concurrentProvisioningIdentity{db: db, identity: auth.Identity{
		ID:       identityID,
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits:   map[string]interface{}{"email": currentEmail},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Via: "email", Value: currentEmail, Verified: true},
			{Via: "email", Value: secondaryEmail, Verified: true},
		},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{currentEmail, secondaryEmail}},
		},
	}}

	spiceDB := testutil.SetupOryStack(t).SpiceDBClient
	member, err := integrationMemberProvisioner(db, identity, spiceDB).ProvisionRegistration(t.Context(), RegistrationMemberInput{
		IdentityID: identityID, Email: currentEmail, PreferredLocale: "pt-PT",
	})
	require.NoError(t, err)
	require.Equal(t, currentEmail, *member.PrimaryEmail)
	require.Equal(t, []string{currentEmail}, []string(member.AvailableEmails))
	require.NotNil(t, member.PreferredLocale)
	require.Equal(t, "pt-PT", *member.PreferredLocale)
}

func TestProvisionRegistrationEnforcesDeletedMemberEmailReuseHold(t *testing.T) {
	db := newServiceIntegrationDB(t)
	const email = "returning@example.test"
	now := time.Now().UTC()
	deletedAt := now.Add(-6 * 24 * time.Hour)
	deletedMemberID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO member (
			id, nickname, onboarded, primary_email, available_emails,
			deleted_at, created_at, updated_at
		) VALUES (?::uuid, ?, TRUE, ?, ARRAY[?]::text[], ?, ?, ?)
	`, deletedMemberID, "Deleted "+deletedMemberID, email, email,
		deletedAt, deletedAt.Add(-24*time.Hour), deletedAt).Error)

	identityID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (id, external_id, schema_id, traits, state, created_at, updated_at)
		VALUES (?::uuid, NULL, 'user', jsonb_build_object('email', ?::text), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, identityID, email).Error)
	identity := &concurrentProvisioningIdentity{db: db, identity: auth.Identity{
		ID:       identityID,
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits:   map[string]interface{}{"email": email},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Via: "email", Value: email, Verified: true,
		}},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{email}},
		},
	}}
	provisioner := integrationMemberProvisioner(db, identity, testutil.SetupOryStack(t).SpiceDBClient)
	input := RegistrationMemberInput{IdentityID: identityID, Email: email, PreferredLocale: "en"}

	_, err := provisioner.ProvisionRegistration(t.Context(), input)
	require.ErrorContains(t, err, "registration cannot be completed")
	var linkedCount int64
	require.NoError(t, db.Model(&model.Member{}).
		Where("account_identity_id = ?", identityID).
		Count(&linkedCount).Error)
	require.Zero(t, linkedCount)

	require.NoError(t, db.Exec(`
		UPDATE member
		SET deleted_at = ?, created_at = ?
		WHERE id = ?::uuid
	`, now.Add(-8*24*time.Hour), now.Add(-9*24*time.Hour), deletedMemberID).Error)
	created, err := provisioner.ProvisionRegistration(t.Context(), input)
	require.NoError(t, err)
	require.NotEqual(t, deletedMemberID, created.ID)
	require.False(t, created.Onboarded)
}

func TestProvisionRegistrationRejectsConflictingExternalIDBeforeMemberCreate(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	conflictingMemberID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (id, external_id, schema_id, traits, state, created_at, updated_at)
		VALUES (?::uuid, ?::text, 'user', jsonb_build_object('email', 'member@example.test'), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, identityID, conflictingMemberID).Error)

	identity := &concurrentProvisioningIdentity{db: db, identity: auth.Identity{
		ID:       identityID,
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits:   map[string]interface{}{"email": "member@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Via: "email", Value: "member@example.test", Verified: true,
		}},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"member@example.test"}},
		},
		ExternalID: conflictingMemberID,
	}}

	_, err := integrationMemberProvisioner(db, identity, testutil.SetupOryStack(t).SpiceDBClient).ProvisionRegistration(t.Context(), RegistrationMemberInput{
		IdentityID: identityID, Email: "member@example.test", PreferredLocale: "en",
	})
	require.ErrorContains(t, err, "identity.external_id conflicts with the member reverse pointer")

	var count int64
	require.NoError(t, db.Model(&model.Member{}).
		Where("account_identity_id = ?", identityID).
		Count(&count).Error)
	require.Zero(t, count)
}

func TestProvisionRegistrationAllowsIdempotentSamePairRetry(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (id, external_id, schema_id, traits, state, created_at, updated_at)
		VALUES (?::uuid, NULL, 'user', jsonb_build_object('email', 'member@example.test'), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, identityID).Error)

	identity := &concurrentProvisioningIdentity{db: db, identity: auth.Identity{
		ID:       identityID,
		SchemaID: "user",
		State:    auth.KratosStateActive,
		Traits:   map[string]interface{}{"email": "member@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Via: "email", Value: "member@example.test", Verified: true,
		}},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"member@example.test"}},
		},
	}}
	provisioner := integrationMemberProvisioner(db, identity, testutil.SetupOryStack(t).SpiceDBClient)
	input := RegistrationMemberInput{
		IdentityID: identityID, Email: "member@example.test", PreferredLocale: "en",
	}

	created, err := provisioner.ProvisionRegistration(t.Context(), input)
	require.NoError(t, err)
	retried, err := provisioner.ProvisionRegistration(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, created.ID, retried.ID)

	var count int64
	require.NoError(t, db.Model(&model.Member{}).
		Where("account_identity_id = ?", identityID).
		Count(&count).Error)
	require.Equal(t, int64(1), count)
}
