//go:build integration

package account

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestOIDCUnlinkRetryConvergesMemberEmailAfterCredentialDeletionIntegration(t *testing.T) {
	t.Parallel()
	db := newConcurrentServiceIntegrationDB(t)
	identityID := uuid.NewString()
	emailAddress := "unlink-retry-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: emailAddress,
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, emailAddress)
	manager := newCredentialMutationIdentityManager(identityID, emailAddress)
	manager.identity.ExternalID = memberID
	service := NewAccountCredentialMutationService(db, identitystate.PostgresFence{}, manager, memberEmailProjectionIntegration{})

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

func TestOIDCUnlinkAllowsPrimaryWhenEmailCodeSharesTheSameAddressIntegration(t *testing.T) {
	t.Parallel()
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

	require.NoError(t, NewAccountCredentialMutationService(db, identitystate.PostgresFence{}, manager, memberEmailProjectionIntegration{}).
		RemoveOIDCProvider(t.Context(), identityID, "google", "subject"))

	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Equal(t, providerEmail, *member.PrimaryEmail)
	require.Equal(t, []string{providerEmail}, []string(member.AvailableEmails))
	require.False(t, auth.NewCredentialInventory(manager.identity.Credentials).HasOIDCProvider("google", "subject"))
}
