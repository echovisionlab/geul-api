package account_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// credentialKratos fails session revocation, or the first credential reload
// after a credential deletion.
type credentialKratos struct {
	*accounttest.Kratos
	revokeErr                       error
	failCredentialReloadOnce        bool
	failCredentialReloadAfterDelete bool
}

func (m *credentialKratos) GetIdentityWithIncludeCredential(ctx context.Context, identityID, credentialType string) (*auth.Identity, error) {
	if m.failCredentialReloadOnce {
		m.failCredentialReloadOnce = false
		return nil, errors.New("identity reload unavailable")
	}
	return m.Kratos.GetIdentityWithIncludeCredential(ctx, identityID, credentialType)
}

func (m *credentialKratos) DeleteIdentitySessions(ctx context.Context, identityID string) error {
	if m.revokeErr != nil {
		return m.revokeErr
	}
	return m.Kratos.DeleteIdentitySessions(ctx, identityID)
}

func (m *credentialKratos) DeleteIdentityCredentialIdentifier(ctx context.Context, identityID, credentialType, identifier string) error {
	if err := m.Kratos.DeleteIdentityCredentialIdentifier(ctx, identityID, credentialType, identifier); err != nil {
		return err
	}
	m.failCredentialReloadOnce = m.failCredentialReloadAfterDelete
	return nil
}

type credentialFixture struct {
	fixture
	kratos     *credentialKratos
	service    *account.AccountCredentialMutationService
	memberID   string
	identityID string
}

// newCredentialFixture seeds a pair whose Identity signs in with a Google
// provider and an email code for email.
func newCredentialFixture(t *testing.T, email func(identityID string) string) credentialFixture {
	t.Helper()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("")
	address := email(identityID)
	f.store.UpdateMember(memberID, func(member *accounttest.Member) {
		member.PrimaryEmail, member.AvailableEmails = address, []string{address}
	})
	f.kratos.Update(identityID, func(identity *auth.Identity) {
		identity.Traits = structured.Fields{"email": address}
		identity.VerifiableAddresses = []auth.VerifiableAddress{{Value: address, Via: "email", Verified: true}}
		identity.Credentials = map[string]auth.Credential{
			"oidc": {
				Type: "oidc", Identifiers: []string{"google:subject"},
				Config: structured.Fields{"providers": structured.Values{structured.Fields{"provider": "google", "subject": "subject"}}},
			},
			"code": {Type: "code", Identifiers: []string{address}},
		}
	})
	kratos := &credentialKratos{Kratos: f.kratos, failCredentialReloadAfterDelete: true}
	service, err := account.NewService(account.Deps{
		Units: f.units, SpiceDB: f.authorizer, Fence: f.fence, Identity: kratos, Emails: f.emails, Members: f.members, Audit: f.audit,
	})
	require.NoError(t, err)
	return credentialFixture{fixture: f, kratos: kratos, service: service.Credentials(), memberID: memberID, identityID: identityID}
}

func (f credentialFixture) hasGoogle() bool {
	return auth.NewCredentialInventory(f.kratos.Identity(f.identityID).Credentials).HasOIDCProvider("google", "subject")
}

func (f credentialFixture) calls(method string) int {
	count := 0
	for _, call := range f.kratos.Calls() {
		if call == method+":"+f.identityID {
			count++
		}
	}
	return count
}

func TestOIDCUnlinkRevokesSessionsBeforeCredentialMutation(t *testing.T) {
	t.Parallel()
	f := newCredentialFixture(t, func(identityID string) string { return "unlink-revoke-" + identityID + "@example.test" })
	f.kratos.revokeErr = errors.New("session revoke unavailable")

	err := f.service.RemoveOIDCProvider(t.Context(), f.identityID, "google", "subject")

	require.ErrorContains(t, err, "session revoke unavailable")
	require.Equal(t, []string{f.identityID}, f.units.FencedIdentities())
	require.Zero(t, f.calls("DeleteIdentityCredentialIdentifier"))
	require.True(t, f.hasGoogle())
}

func TestOIDCUnlinkRejectsRemovingOnlySourceOfMemberPrimary(t *testing.T) {
	t.Parallel()
	var providerEmail string
	f := newCredentialFixture(t, func(identityID string) string {
		providerEmail = "unlink-primary-" + identityID + "@example.test"
		return providerEmail
	})
	f.kratos.Update(f.identityID, func(identity *auth.Identity) {
		identity.Credentials["oidc"] = auth.Credential{
			Type: "oidc", Identifiers: []string{"google:subject"},
			Config: structured.Fields{"providers": structured.Values{structured.Fields{
				"provider": "google", "subject": "subject", "email": providerEmail, "email_verified": true,
			}}},
		}
		identity.Credentials["code"] = auth.Credential{Type: "code", Identifiers: []string{"recovery@example.test"}}
	})

	err := f.service.RemoveOIDCProvider(t.Context(), f.identityID, "google", "subject")

	require.ErrorContains(t, err, "choose another account email")
	require.Zero(t, f.calls("DeleteIdentityCredentialIdentifier"))
	require.True(t, f.hasGoogle())
}

func TestOIDCUnlinkRetryConvergesMemberEmailAfterCredentialDeletion(t *testing.T) {
	t.Parallel()
	var emailAddress string
	f := newCredentialFixture(t, func(identityID string) string {
		emailAddress = "unlink-retry-" + identityID + "@example.test"
		return emailAddress
	})

	require.ErrorContains(t, f.service.RemoveOIDCProvider(t.Context(), f.identityID, "google", "subject"), "identity reload unavailable")
	require.Equal(t, 1, f.calls("DeleteIdentityCredentialIdentifier"))
	require.Equal(t, 1, f.calls("DeleteIdentitySessions"))
	require.False(t, f.hasGoogle())

	require.NoError(t, f.service.RemoveOIDCProvider(t.Context(), f.identityID, "google", "subject"))
	require.Equal(t, 1, f.calls("DeleteIdentityCredentialIdentifier"))
	require.Equal(t, 1, f.calls("DeleteIdentitySessions"))

	member, found := f.store.Member(f.memberID)
	require.True(t, found)
	require.Equal(t, emailAddress, member.PrimaryEmail)
	require.Equal(t, []string{emailAddress}, member.AvailableEmails)
}

func TestOIDCUnlinkAllowsPrimaryWhenEmailCodeSharesTheSameAddress(t *testing.T) {
	t.Parallel()
	var providerEmail string
	f := newCredentialFixture(t, func(identityID string) string {
		providerEmail = "shared-" + identityID + "@example.test"
		return providerEmail
	})
	f.kratos.failCredentialReloadAfterDelete = false
	f.kratos.Update(f.identityID, func(identity *auth.Identity) {
		identity.Credentials["oidc"] = auth.Credential{
			Type: "oidc", Identifiers: []string{"google:subject"},
			Config: structured.Fields{"providers": structured.Values{structured.Fields{
				"provider": "google", "subject": "subject", "email": providerEmail, "email_verified": true,
			}}},
		}
	})

	require.NoError(t, f.service.RemoveOIDCProvider(t.Context(), f.identityID, "google", "subject"))

	member, found := f.store.Member(f.memberID)
	require.True(t, found)
	require.Equal(t, providerEmail, member.PrimaryEmail)
	require.Equal(t, []string{providerEmail}, member.AvailableEmails)
	require.False(t, f.hasGoogle())
}
