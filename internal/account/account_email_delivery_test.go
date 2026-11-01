package account_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// identityGetter answers the base read and the credential-scoped read with
// fixed Identities.
type identityGetter struct {
	base     *auth.Identity
	included *auth.Identity
}

func (m identityGetter) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.base, nil
}

func (m identityGetter) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return m.included, nil
}

// readStep runs a Member projection read in one snapshot of units.
func readStep(units *accounttest.Units) account.StepFunc {
	return func(ctx context.Context, work func(account.Store) error) error { return units.Read(ctx, work) }
}

// seededProjection stores a linked pair whose Member primary email is
// primaryEmail.
func seededProjection(memberID, identityID, primaryEmail string) *accounttest.Units {
	store, units := accounttest.New()
	store.SeedPair(memberID, identityID, primaryEmail)
	return units
}

func TestResolveMemberPrimaryEmailForIdentityKeepsCanonicalProvenAddress(t *testing.T) {
	t.Parallel()
	identityID := "11111111-1111-4111-8111-111111111111"
	memberID := "22222222-2222-4222-8222-222222222222"
	units := seededProjection(memberID, identityID, "delivery@example.com")
	identity := &auth.Identity{
		ID:         identityID,
		ExternalID: memberID,
		Traits:     structured.Fields{"email": "delivery@example.com"},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Value: "canonical@example.com", Via: "email", Verified: true},
			{Value: "delivery@example.com", Via: "email", Verified: true},
		},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"canonical@example.com", "delivery@example.com"}},
		},
	}

	accountEmail, reason, err := account.ResolveMemberPrimaryEmailForIdentity(
		t.Context(), readStep(units), identityGetter{base: identity, included: identity}, identityID,
	)

	require.NoError(t, err)
	require.Empty(t, reason)
	require.NotNil(t, accountEmail)
	require.Equal(t, "delivery@example.com", accountEmail.Email)
	require.Same(t, identity, accountEmail.Identity)
}

func TestResolveMemberPrimaryEmailForIdentityRejectsStaleMemberProjection(t *testing.T) {
	t.Parallel()
	identityID := "55555555-5555-4555-8555-555555555555"
	memberID := "66666666-6666-4666-8666-666666666666"
	units := seededProjection(memberID, identityID, "stale@example.com")
	identity := &auth.Identity{
		ID:         identityID,
		ExternalID: memberID,
		Traits:     structured.Fields{"email": "canonical@example.com"},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Value: "canonical@example.com", Via: "email", Verified: true},
		},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"canonical@example.com"}},
		},
	}

	accountEmail, reason, err := account.ResolveMemberPrimaryEmailForIdentity(
		t.Context(), readStep(units), identityGetter{base: identity, included: identity}, identityID,
	)

	require.NoError(t, err)
	require.Nil(t, accountEmail)
	require.Equal(t, account.AccountEmailSkipReasonCanonicalMismatch, reason)
}

func TestResolveMemberPrimaryEmailForIdentityIncludesOIDCCredentialProof(t *testing.T) {
	t.Parallel()
	identityID := "33333333-3333-4333-8333-333333333333"
	memberID := "44444444-4444-4444-8444-444444444444"
	units := seededProjection(memberID, identityID, "provider@example.com")
	identity := &auth.Identity{
		ID:         identityID,
		ExternalID: memberID,
		Traits:     structured.Fields{"email": "provider@example.com"},
		Credentials: map[string]auth.Credential{
			"oidc": {
				Type: "oidc",
				Config: structured.Fields{
					"providers": structured.Values{structured.Fields{
						"provider": "google",
						"subject":  "google-subject",
						"claims": structured.Fields{
							"email":          "provider@example.com",
							"email_verified": true,
						},
					}},
				},
			},
		},
	}

	accountEmail, reason, err := account.ResolveMemberPrimaryEmailForIdentity(t.Context(), readStep(units), identityGetter{
		base: &auth.Identity{
			ID:         identityID,
			ExternalID: memberID,
			Traits:     structured.Fields{"email": "provider@example.com"},
		},
		included: identity,
	}, identityID)

	require.NoError(t, err)
	require.Empty(t, reason)
	require.NotNil(t, accountEmail)
	require.Equal(t, "provider@example.com", accountEmail.Email)
}
