package account

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestProjectedAccountEmailRowsIncludePendingCandidatesAfterUsableCandidates(t *testing.T) {
	identity := &auth.Identity{
		ID: "identity-1",
		Traits: structured.Fields{
			"email": "CURRENT@Example.test",
		},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Value: "alternate@example.test", Via: "email", Verified: true},
			{Value: "CURRENT@example.test", Via: "email", Verified: true},
			{Value: "unverified@example.test", Via: "email", Verified: false},
		},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"current@example.test", "alternate@example.test"}},
			"oidc": {
				Type: "oidc",
				Config: structured.Fields{
					"providers": structured.Values{structured.Fields{
						"provider": "google", "subject": "subject-1",
						"email": "provider@example.test", "email_verified": true,
					}},
				},
			},
		},
	}

	rows := projectedAccountEmailRows(identity, nil)

	require.Equal(t, []string{
		"current@example.test",
		"provider@example.test",
		"alternate@example.test",
		"unverified@example.test",
	}, projectedEmailAddresses(rows))
	pending := findProjectionRow(rows, "unverified@example.test")
	require.NotNil(t, pending)
	require.False(t, pending.UsableForDelivery)
	require.True(t, rows[0].IsCurrentEmail)
	require.True(t, rows[0].IdentityVerified)
	require.True(t, rows[1].EffectiveTrusted)
	require.Equal(t, "google", ptrStringValue(rows[1].Sources[0].Provider))
	require.False(t, rows[2].EffectiveTrusted, "a non-canonical code identifier is not a delivery authority")
}

func TestProjectedAccountEmailRowsIncludesUnverifiedProviderCandidateAsPending(t *testing.T) {
	identity := &auth.Identity{
		Traits: structured.Fields{"email": "current@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Value: "current@example.test", Via: "email", Verified: true,
		}},
	}
	rows := projectedAccountEmailRows(identity, []AccountEmailProviderCandidate{
		{Email: "unverified@example.test", Provider: "github", Verified: false},
	})
	require.Len(t, rows, 2)
	require.Equal(t, "current@example.test", rows[0].NormalizedEmail)
	require.Equal(t, "unverified@example.test", rows[1].NormalizedEmail)
	require.False(t, rows[1].UsableForDelivery)
}

func TestProjectedAccountEmailRowsIgnoresNonCanonicalEmailCodeIdentifier(t *testing.T) {
	identity := &auth.Identity{
		Traits: structured.Fields{"email": "sso@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Value: "sso@example.test", Via: "email", Verified: true,
		}},
		Credentials: map[string]auth.Credential{
			"oidc": {Type: "oidc", Config: structured.Fields{
				"providers": structured.Values{structured.Fields{
					"provider": "google", "subject": "subject-1", "email": "shared@example.test", "email_verified": true,
				}},
			}},
			"code": {Type: "code", Identifiers: []string{"shared@example.test"}},
		},
	}

	row := findProjectionRow(projectedAccountEmailRows(identity, nil), "shared@example.test")
	require.NotNil(t, row)
	require.True(t, row.UsableForDelivery)
	require.Len(t, row.Sources, 1)
	require.Equal(t, model.AccountEmailSourceTypeOIDCProvider, row.Sources[0].SourceType)
}

func TestProjectedAccountEmailRowsDoesNotTreatRawVerifiedAddressAsSource(t *testing.T) {
	identity := &auth.Identity{
		Traits: structured.Fields{"email": "raw@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Value: "raw@example.test", Via: "email", Verified: true,
		}},
	}

	row := findProjectionRow(projectedAccountEmailRows(identity, nil), "raw@example.test")
	require.NotNil(t, row)
	require.True(t, row.IdentityVerified)
	require.False(t, row.UsableForDelivery)
}

func TestValidateRegistrationIdentityAcceptsVerifiedProviderWithoutEmailCode(t *testing.T) {
	identityID := uuid.NewString()
	identity := &auth.Identity{
		ID: identityID, State: auth.KratosStateActive,
		Traits: structured.Fields{"email": "provider@example.test"},
		Credentials: map[string]auth.Credential{"oidc": {
			Type: "oidc",
			Config: structured.Fields{"providers": structured.Values{structured.Fields{
				"provider": "google", "subject": "subject-1",
				"email": "provider@example.test", "email_verified": true,
			}}},
		}},
	}

	require.NoError(t, validateRegistrationIdentity(
		RegistrationMemberInput{IdentityID: identityID, Email: "provider@example.test"},
		identity,
		nil,
	))
}

func TestValidateRegistrationIdentityRejectsRawVerifiedAddressWithoutSource(t *testing.T) {
	identityID := uuid.NewString()
	identity := &auth.Identity{
		ID: identityID, State: auth.KratosStateActive,
		Traits: structured.Fields{"email": "raw@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Value: "raw@example.test", Via: "email", Verified: true,
		}},
	}

	err := validateRegistrationIdentity(
		RegistrationMemberInput{IdentityID: identityID, Email: "raw@example.test"},
		identity,
		nil,
	)
	require.ErrorContains(t, err, "not proven")
}

func TestLoadIdentityWithCredentialsPreservesOIDCConfig(t *testing.T) {
	manager := &accountEmailCredentialMergeIdentityManager{byCredential: map[string]*auth.Identity{
		"oidc": {
			ID: "identity-1",
			Credentials: map[string]auth.Credential{"oidc": {
				Type: "oidc",
				Config: structured.Fields{"providers": structured.Values{structured.Fields{
					"provider": "google", "subject": "google-subject",
				}}},
			}},
		},
	}}
	identity, err := LoadIdentityWithEmailCredentials(context.Background(), manager, "identity-1")
	require.NoError(t, err)
	require.Contains(t, identity.Credentials, "oidc")
}

func TestLoadIdentityWithCredentialsMergesOnlyRequestedCredentialViews(t *testing.T) {
	manager := &accountEmailCredentialMergeIdentityManager{byCredential: map[string]*auth.Identity{
		"oidc": {
			ID: "identity-1",
			Credentials: map[string]auth.Credential{
				"oidc": {Type: "oidc"},
			},
		},
		"code": {
			ID: "identity-1",
			Credentials: map[string]auth.Credential{
				"code": {Type: "code", Identifiers: []string{"user@example.test"}},
			},
		},
		"passkey": {
			ID: "identity-1",
			Credentials: map[string]auth.Credential{
				"passkey": {Type: "passkey"},
			},
		},
	}}

	identity, err := loadIdentityWithCredentials(
		context.Background(), manager, "identity-1", "code", "passkey",
	)
	require.NoError(t, err)
	require.Equal(t, "identity-1", identity.ID)
	require.NotContains(t, identity.Credentials, "oidc")
	require.Contains(t, identity.Credentials, "code")
	require.Contains(t, identity.Credentials, "passkey")
}

func TestAccountEmailProjectionRequiresDependencies(t *testing.T) {
	service := &AccountEmailService{}

	_, err := service.SyncMemberEmailProjection(t.Context(), "identity-1", &auth.Identity{ID: "identity-1"}, nil)
	require.ErrorContains(t, err, "member database is required")

	err = service.EnsureMemberPrimaryEmailUsable(t.Context(), "identity-1", &auth.Identity{ID: "identity-1"}, nil)
	require.ErrorContains(t, err, "member database is required")
}

func projectedEmailAddresses(rows []AccountEmailProjection) []string {
	result := make([]string, len(rows))
	for i := range rows {
		result[i] = rows[i].NormalizedEmail
	}
	return result
}

type accountEmailCredentialMergeIdentityManager struct {
	byCredential map[string]*auth.Identity
}

type accountEmailCollisionIdentityManager struct {
	found *auth.Identity
}

func (m accountEmailCollisionIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.found, nil
}

func (m accountEmailCollisionIdentityManager) GetIdentityWithIncludeCredential(
	context.Context, string, string,
) (*auth.Identity, error) {
	return m.found, nil
}

func (m accountEmailCollisionIdentityManager) FindIdentityByCredentialIdentifier(
	context.Context, string,
) (*auth.Identity, bool, error) {
	return m.found, m.found != nil, nil
}

func TestEmailCodeAddressUsedByAnotherIdentityUsesCredentialIdentifierAuthority(t *testing.T) {
	finder := accountEmailCollisionIdentityManager{found: &auth.Identity{ID: "other-identity"}}
	used, err := emailCodeAddressUsedByAnotherIdentity(
		t.Context(), finder, "current-identity", "shared@example.test",
	)
	require.NoError(t, err)
	require.True(t, used)

	finder = accountEmailCollisionIdentityManager{found: &auth.Identity{ID: "current-identity"}}
	used, err = emailCodeAddressUsedByAnotherIdentity(
		t.Context(), finder, "current-identity", "shared@example.test",
	)
	require.NoError(t, err)
	require.False(t, used)
}

func TestEmailCodeAddressUsedByAnotherIdentityRequiresExactDependencies(t *testing.T) {
	_, err := emailCodeAddressUsedByAnotherIdentity(t.Context(), nil, "current-identity", "user@example.test")
	require.ErrorContains(t, err, "identity credential finder is required")

	_, err = emailCodeAddressUsedByAnotherIdentity(
		t.Context(),
		accountEmailCollisionIdentityManager{},
		"",
		"user@example.test",
	)
	require.ErrorContains(t, err, "identity id is required")

	_, err = emailCodeAddressUsedByAnotherIdentity(
		t.Context(),
		accountEmailCollisionIdentityManager{found: nil},
		"current-identity",
		"user@example.test",
	)
	require.NoError(t, err)
}

func (m *accountEmailCredentialMergeIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return nil, nil
}

func (m *accountEmailCredentialMergeIdentityManager) GetIdentityWithIncludeCredential(_ context.Context, _ string, credentialType string) (*auth.Identity, error) {
	return m.byCredential[credentialType], nil
}
