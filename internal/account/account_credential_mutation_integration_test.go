//go:build integration

package account

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

type credentialMutationIdentityManager struct {
	*adminAuthIdentityManager
	revokeErr                       error
	failCredentialReloadOnce        bool
	failCredentialReloadAfterDelete bool
}

func (m *credentialMutationIdentityManager) GetIdentityWithIncludeCredential(
	ctx context.Context,
	identityID string,
	credentialType string,
) (*auth.Identity, error) {
	if m.failCredentialReloadOnce {
		m.failCredentialReloadOnce = false
		return nil, errors.New("identity reload unavailable")
	}
	return m.adminAuthIdentityManager.GetIdentityWithIncludeCredential(ctx, identityID, credentialType)
}

func (m *credentialMutationIdentityManager) DeleteIdentitySessions(ctx context.Context, identityID string) error {
	if m.revokeErr != nil {
		return m.revokeErr
	}
	return m.adminAuthIdentityManager.DeleteIdentitySessions(ctx, identityID)
}

func (m *credentialMutationIdentityManager) DeleteIdentityCredentialIdentifier(
	ctx context.Context,
	identityID string,
	credentialType string,
	identifier string,
) error {
	if err := m.adminAuthIdentityManager.DeleteIdentityCredentialIdentifier(
		ctx,
		identityID,
		credentialType,
		identifier,
	); err != nil {
		return err
	}
	m.failCredentialReloadOnce = m.failCredentialReloadAfterDelete
	return nil
}

func TestOIDCUnlinkRevokesSessionsBeforeCredentialMutationIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	identityID := uuid.NewString()
	emailAddress := "unlink-revoke-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: emailAddress,
	})
	manager := newCredentialMutationIdentityManager(identityID, emailAddress)
	manager.revokeErr = errors.New("session revoke unavailable")

	err := NewAccountCredentialMutationService(db, manager, memberEmailProjectionIntegration{}).
		RemoveOIDCProvider(t.Context(), identityID, "google", "subject")

	require.ErrorContains(t, err, "session revoke unavailable")
	require.Empty(t, manager.deletedCredentialIdentifiers)
	require.True(t, auth.NewCredentialInventory(manager.identity.Credentials).
		HasOIDCProvider("google", "subject"))
}

func TestOIDCUnlinkRetryConvergesMemberEmailAfterCredentialDeletionIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	identityID := uuid.NewString()
	emailAddress := "unlink-retry-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: emailAddress,
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, emailAddress)
	manager := newCredentialMutationIdentityManager(identityID, emailAddress)
	manager.identity.ExternalID = memberID
	service := NewAccountCredentialMutationService(db, manager, memberEmailProjectionIntegration{})

	require.ErrorContains(
		t,
		service.RemoveOIDCProvider(t.Context(), identityID, "google", "subject"),
		"identity reload unavailable",
	)
	require.Len(t, manager.deletedCredentialIdentifiers, 1)
	require.Len(t, manager.deletedSessions, 1)
	require.False(t, auth.NewCredentialInventory(manager.identity.Credentials).
		HasOIDCProvider("google", "subject"))

	require.NoError(t, service.RemoveOIDCProvider(t.Context(), identityID, "google", "subject"))
	require.Len(t, manager.deletedCredentialIdentifiers, 1)
	require.Len(t, manager.deletedSessions, 1)

	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Equal(t, emailAddress, *member.PrimaryEmail)
	require.Equal(t, []string{emailAddress}, []string(member.AvailableEmails))
}

func TestOIDCUnlinkRejectsRemovingOnlySourceOfMemberPrimaryIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	identityID := uuid.NewString()
	providerEmail := "unlink-primary-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: providerEmail,
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, providerEmail)
	manager := newCredentialMutationIdentityManager(identityID, providerEmail)
	manager.identity.ExternalID = memberID
	manager.identity.Credentials["oidc"] = auth.Credential{
		Type:        "oidc",
		Identifiers: []string{"google:subject"},
		Config: map[string]interface{}{"providers": []interface{}{map[string]interface{}{
			"provider": "google", "subject": "subject", "email": providerEmail, "email_verified": true,
		}}},
	}
	manager.identity.Credentials["code"] = auth.Credential{Type: "code", Identifiers: []string{"recovery@example.test"}}

	err := NewAccountCredentialMutationService(db, manager, memberEmailProjectionIntegration{}).
		RemoveOIDCProvider(t.Context(), identityID, "google", "subject")

	require.ErrorContains(t, err, "choose another account email")
	require.Empty(t, manager.deletedCredentialIdentifiers)
	require.True(t, auth.NewCredentialInventory(manager.identity.Credentials).HasOIDCProvider("google", "subject"))
}

func TestOIDCUnlinkAllowsPrimaryWhenEmailCodeSharesTheSameAddressIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	identityID := uuid.NewString()
	providerEmail := "shared-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: providerEmail,
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, providerEmail)
	manager := newCredentialMutationIdentityManager(identityID, providerEmail)
	manager.failCredentialReloadAfterDelete = false
	manager.identity.ExternalID = memberID
	manager.identity.Credentials["oidc"] = auth.Credential{
		Type:        "oidc",
		Identifiers: []string{"google:subject"},
		Config: map[string]interface{}{"providers": []interface{}{map[string]interface{}{
			"provider": "google", "subject": "subject", "email": providerEmail, "email_verified": true,
		}}},
	}
	manager.identity.Credentials["code"] = auth.Credential{Type: "code", Identifiers: []string{providerEmail}}
	manager.identity.VerifiableAddresses = []auth.VerifiableAddress{{Via: "email", Value: providerEmail, Verified: true}}

	require.NoError(t, NewAccountCredentialMutationService(db, manager, memberEmailProjectionIntegration{}).
		RemoveOIDCProvider(t.Context(), identityID, "google", "subject"))

	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Equal(t, providerEmail, *member.PrimaryEmail)
	require.Equal(t, []string{providerEmail}, []string(member.AvailableEmails))
	require.False(t, auth.NewCredentialInventory(manager.identity.Credentials).HasOIDCProvider("google", "subject"))
}

func newCredentialMutationIdentityManager(
	identityID string,
	emailAddress string,
) *credentialMutationIdentityManager {
	return &credentialMutationIdentityManager{failCredentialReloadAfterDelete: true, adminAuthIdentityManager: &adminAuthIdentityManager{
		credentialScoped: true,
		identity: &auth.Identity{
			ID: identityID,
			Traits: map[string]interface{}{
				"email": emailAddress,
			},
			VerifiableAddresses: []auth.VerifiableAddress{{
				Value: emailAddress, Via: "email", Verified: true,
			}},
			Credentials: map[string]auth.Credential{
				"oidc": {
					Type:        "oidc",
					Identifiers: []string{"google:subject"},
					Config: map[string]interface{}{
						"providers": []interface{}{map[string]interface{}{
							"provider": "google", "subject": "subject",
						}},
					},
				},
				"code": {Type: "code", Identifiers: []string{emailAddress}},
			},
		},
	}}
}
