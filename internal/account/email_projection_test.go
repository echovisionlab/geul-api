package account_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// projectionFixture stores a linked pair whose Member projection holds
// primary and available.
func projectionFixture(primary string, available ...string) (*accounttest.Store, *accounttest.Units, string, string) {
	store, units := accounttest.New()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	store.SeedPair(memberID, identityID, primary)
	store.UpdateMember(memberID, func(member *accounttest.Member) { member.AvailableEmails = available })
	return store, units, memberID, identityID
}

func TestSyncMemberEmailProjectionReplacesExactProvenCandidateSet(t *testing.T) {
	t.Parallel()
	store, units, memberID, identityID := projectionFixture("stale@example.test", "stale@example.test")
	primaryEmail := "primary-" + identityID + "@example.test"
	identity := &auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: structured.Fields{"email": primaryEmail},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{primaryEmail, "alternate@example.test"}},
		},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Via: "email", Value: primaryEmail, Verified: true},
			{Via: "email", Value: "alternate@example.test", Verified: true},
			{Via: "email", Value: "pending@example.test", Verified: false},
		},
	}

	rows, err := account.NewAccountEmailService(identityGetter{base: identity, included: identity}, units).
		SyncMemberEmailProjection(t.Context(), identityID, identity, nil)

	require.NoError(t, err)
	require.Len(t, rows, 3)
	pending := account.FindAccountEmailProjection(rows, "pending@example.test")
	require.NotNil(t, pending)
	require.False(t, pending.UsableForDelivery)
	member, _ := store.Member(memberID)
	require.Equal(t, primaryEmail, member.PrimaryEmail)
	require.Equal(t, []string{primaryEmail}, member.AvailableEmails)
}

func TestSyncMemberEmailProjectionRejectsUnusableCanonical(t *testing.T) {
	t.Parallel()
	store, units, memberID, identityID := projectionFixture("stale@example.test", "stale@example.test")
	unprovenEmail := "unproven-" + identityID + "@example.test"
	identity := &auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: structured.Fields{"email": unprovenEmail},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"zulu@example.test", "alpha@example.test"}},
		},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Via: "email", Value: "zulu@example.test", Verified: true},
			{Via: "email", Value: "alpha@example.test", Verified: true},
		},
	}

	_, err := account.NewAccountEmailService(identityGetter{base: identity, included: identity}, units).
		SyncMemberEmailProjection(t.Context(), identityID, identity, nil)

	require.ErrorContains(t, err, "has no proven usable email candidate")
	member, _ := store.Member(memberID)
	require.Equal(t, "stale@example.test", member.PrimaryEmail)
	require.Empty(t, units.Steps(), "the projection is rejected before its transaction")
}

func TestSyncAccountEmailCandidateProjectionDoesNotMutateIdentityTraits(t *testing.T) {
	t.Parallel()
	deliveryEmail := "delivery-" + uuid.NewString() + "@example.test"
	store, units, memberID, identityID := projectionFixture(deliveryEmail, deliveryEmail)
	credentials := map[string]auth.Credential{
		"oidc": {
			Type:        "oidc",
			Identifiers: []string{"google:google-subject", "github:github-subject"},
			Config: structured.Fields{"providers": structured.Values{
				structured.Fields{"provider": "google", "subject": "google-subject", "email": " Google@Example.Test ", "email_verified": true},
				structured.Fields{
					"provider": "github", "subject": "github-subject", "primary_verified_email": "GitHub@Example.Test",
					"email": "ignored-unverified-github@example.test", "verified": false, "primary": true,
				},
			}},
		},
		"code": {Type: "code", Identifiers: []string{deliveryEmail}},
	}
	identity := &auth.Identity{
		ID: identityID, ExternalID: memberID,
		Traits:              structured.Fields{"email": deliveryEmail},
		VerifiableAddresses: []auth.VerifiableAddress{{Via: "email", Value: deliveryEmail, Verified: true}},
		Credentials:         credentials,
	}
	providerCandidates := account.ResolveAccountEmailProviderCandidates(t.Context(), credentials)
	emailService := account.NewAccountEmailService(identityGetter{base: identity, included: identity}, units)

	require.NoError(t, emailService.EnsureMemberPrimaryEmailUsable(t.Context(), identity.ID, identity, providerCandidates))
	_, err := emailService.SyncMemberEmailProjection(t.Context(), identity.ID, identity, providerCandidates)
	require.NoError(t, err)

	require.Equal(t, structured.Fields{"email": deliveryEmail}, identity.Traits)
	member, _ := store.Member(memberID)
	require.Equal(t, deliveryEmail, member.PrimaryEmail)
	require.Equal(t, []string{deliveryEmail, "github@example.test", "google@example.test"}, member.AvailableEmails)
}
