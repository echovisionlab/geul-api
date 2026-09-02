package account

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type deliveryCredentialIdentityManager struct {
	base     *auth.Identity
	included *auth.Identity
}

func (m deliveryCredentialIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.base, nil
}

func (m deliveryCredentialIdentityManager) GetIdentityWithIncludeCredential(
	context.Context,
	string,
	string,
) (*auth.Identity, error) {
	return m.included, nil
}

func TestResolveMemberPrimaryEmailForIdentityKeepsCanonicalProvenAddress(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			primary_email TEXT,
			deleted_at DATETIME
		)
	`).Error)
	identityID := "11111111-1111-4111-8111-111111111111"
	memberID := "22222222-2222-4222-8222-222222222222"
	require.NoError(t, db.Exec(
		"INSERT INTO member (id, account_identity_id, primary_email) VALUES (?, ?, ?)",
		memberID, identityID, "delivery@example.com",
	).Error)
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

	accountEmail, reason, err := ResolveMemberPrimaryEmailForIdentity(
		t.Context(), db, memberEmailProjectionStub{primary: "delivery@example.com"}, &fakeIdentityManager{identity: identity}, identityID,
	)

	require.NoError(t, err)
	require.Empty(t, reason)
	require.NotNil(t, accountEmail)
	require.Equal(t, "delivery@example.com", accountEmail.Email)
	require.Same(t, identity, accountEmail.Identity)
}

func TestResolveMemberPrimaryEmailForIdentityRejectsStaleMemberProjection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			primary_email TEXT,
			deleted_at DATETIME
		)
	`).Error)
	identityID := "55555555-5555-4555-8555-555555555555"
	memberID := "66666666-6666-4666-8666-666666666666"
	require.NoError(t, db.Exec(
		"INSERT INTO member (id, account_identity_id, primary_email) VALUES (?, ?, ?)",
		memberID, identityID, "stale@example.com",
	).Error)
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

	accountEmail, reason, err := ResolveMemberPrimaryEmailForIdentity(
		t.Context(), db, memberEmailProjectionStub{primary: "stale@example.com"}, &fakeIdentityManager{identity: identity}, identityID,
	)

	require.NoError(t, err)
	require.Nil(t, accountEmail)
	require.Equal(t, AccountEmailSkipReasonCanonicalMismatch, reason)
}

func TestIdentityHasUsableDeliveryEmailRefreshesLegacyProviderAssertion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer provider-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"email":"provider@example.com","email_verified":true}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	previousEndpoint := googleUserInfoEndpoint
	googleUserInfoEndpoint = server.URL
	t.Cleanup(func() { googleUserInfoEndpoint = previousEndpoint })

	identity := &auth.Identity{
		Traits: structured.Fields{"email": "provider@example.com"},
		Credentials: map[string]auth.Credential{
			"oidc": {
				Type: "oidc",
				Config: structured.Fields{"providers": structured.Values{structured.Fields{
					"provider":             "google",
					"subject":              "provider-subject",
					"initial_access_token": "provider-token",
				}}},
			},
		},
	}

	require.True(t, IdentityHasUsableDeliveryEmail(t.Context(), identity, "provider@example.com"))
}

func TestResolveMemberPrimaryEmailForIdentityIncludesOIDCCredentialProof(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			primary_email TEXT,
			deleted_at DATETIME
		)
	`).Error)
	identityID := "33333333-3333-4333-8333-333333333333"
	memberID := "44444444-4444-4444-8444-444444444444"
	require.NoError(t, db.Exec(
		"INSERT INTO member (id, account_identity_id, primary_email) VALUES (?, ?, ?)",
		memberID, identityID, "provider@example.com",
	).Error)
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

	accountEmail, reason, err := ResolveMemberPrimaryEmailForIdentity(t.Context(), db, memberEmailProjectionStub{primary: "provider@example.com"}, deliveryCredentialIdentityManager{
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
