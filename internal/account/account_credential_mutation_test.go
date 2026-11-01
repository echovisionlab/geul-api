package account

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

// newFencedCredentialMutations runs credential mutations under an in-memory
// identity fence over a database they must not need.
func newFencedCredentialMutations(t *testing.T, manager *credentialMutationIdentityManager) (*AccountCredentialMutationService, *identitystatetest.Fence) {
	t.Helper()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	fence := &identitystatetest.Fence{}
	return NewAccountCredentialMutationService(db, fence, manager, &memberEmailProjectionStub{}), fence
}

func TestOIDCUnlinkRevokesSessionsBeforeCredentialMutation(t *testing.T) {
	t.Parallel()
	identityID := uuid.NewString()
	manager := newCredentialMutationIdentityManager(identityID, "unlink-revoke-"+identityID+"@example.test")
	manager.revokeErr = errors.New("session revoke unavailable")
	service, fence := newFencedCredentialMutations(t, manager)

	err := service.RemoveOIDCProvider(t.Context(), identityID, "google", "subject")

	require.ErrorContains(t, err, "session revoke unavailable")
	require.Equal(t, []string{identityID}, fence.Locked())
	require.Empty(t, manager.deletedCredentialIdentifiers)
	require.True(t, auth.NewCredentialInventory(manager.identity.Credentials).
		HasOIDCProvider("google", "subject"))
}

func TestOIDCUnlinkRejectsRemovingOnlySourceOfMemberPrimary(t *testing.T) {
	t.Parallel()
	identityID := uuid.NewString()
	providerEmail := "unlink-primary-" + identityID + "@example.test"
	manager := newCredentialMutationIdentityManager(identityID, providerEmail)
	manager.identity.ExternalID = uuid.NewString()
	manager.identity.Credentials["oidc"] = auth.Credential{
		Type:        "oidc",
		Identifiers: []string{"google:subject"},
		Config: map[string]interface{}{"providers": []interface{}{map[string]interface{}{
			"provider": "google", "subject": "subject", "email": providerEmail, "email_verified": true,
		}}},
	}
	manager.identity.Credentials["code"] = auth.Credential{Type: "code", Identifiers: []string{"recovery@example.test"}}
	service, _ := newFencedCredentialMutations(t, manager)

	err := service.RemoveOIDCProvider(t.Context(), identityID, "google", "subject")

	require.ErrorContains(t, err, "choose another account email")
	require.Empty(t, manager.deletedCredentialIdentifiers)
	require.True(t, auth.NewCredentialInventory(manager.identity.Credentials).HasOIDCProvider("google", "subject"))
}
