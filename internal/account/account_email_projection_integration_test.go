//go:build integration

package account

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type staticAccountEmailIdentity struct{ identity *auth.Identity }

func (s staticAccountEmailIdentity) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return s.identity, nil
}
func (s staticAccountEmailIdentity) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return s.identity, nil
}

type memberEmailProjectionIntegration struct{}

func (memberEmailProjectionIntegration) PrimaryEmail(ctx context.Context, db *gorm.DB, memberID, identityID string) (string, error) {
	return memberdomain.PrimaryEmail(ctx, db, memberID, identityID)
}

func (memberEmailProjectionIntegration) SyncEmailProjection(ctx context.Context, db *gorm.DB, memberID, identityID, primary string, available []string) error {
	return memberdomain.SyncEmailProjection(ctx, db, memberID, identityID, primary, available)
}

func TestSyncMemberEmailProjectionReplacesExactProvenCandidateSet(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	seedAccountEmailProjectionPair(t, db, identityID, memberID, "primary@example.test")
	require.NoError(t, db.Exec(`INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails) VALUES (?::uuid, ?::uuid, 'Active member', TRUE, 'stale@example.test', ARRAY['stale@example.test']::text[])`, memberID, identityID).Error)
	identity := &auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: map[string]interface{}{"email": "primary@example.test"},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"primary@example.test", "alternate@example.test"}},
		},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Via: "email", Value: "primary@example.test", Verified: true},
			{Via: "email", Value: "alternate@example.test", Verified: true},
			{Via: "email", Value: "pending@example.test", Verified: false},
		},
	}
	rows, err := NewAccountEmailService(db, staticAccountEmailIdentity{identity}, memberEmailProjectionIntegration{}).SyncMemberEmailProjection(t.Context(), identityID, identity, nil)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	pending := FindAccountEmailProjection(rows, "pending@example.test")
	require.NotNil(t, pending)
	require.False(t, pending.UsableForDelivery)
	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Equal(t, "primary@example.test", *member.PrimaryEmail)
	require.Equal(t, []string{"primary@example.test"}, []string(member.AvailableEmails))
}

func TestSyncMemberEmailProjectionRejectsUnusableCanonical(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	seedAccountEmailProjectionPair(t, db, identityID, memberID, "unproven@example.test")
	require.NoError(t, db.Exec(`INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails) VALUES (?::uuid, ?::uuid, 'Active member', TRUE, 'stale@example.test', ARRAY['stale@example.test']::text[])`, memberID, identityID).Error)
	identity := &auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: map[string]interface{}{"email": "unproven@example.test"},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{"zulu@example.test", "alpha@example.test"}},
		},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Via: "email", Value: "zulu@example.test", Verified: true},
			{Via: "email", Value: "alpha@example.test", Verified: true},
		},
	}
	_, err := NewAccountEmailService(db, staticAccountEmailIdentity{identity}, memberEmailProjectionIntegration{}).SyncMemberEmailProjection(t.Context(), identityID, identity, nil)
	require.ErrorContains(t, err, "has no proven usable email candidate")
	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Equal(t, "stale@example.test", *member.PrimaryEmail)
}

func seedAccountEmailProjectionPair(
	t *testing.T,
	db *gorm.DB,
	identityID, memberID, email string,
) {
	t.Helper()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email,
	})
	require.NoError(t, db.Exec(
		`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`,
		memberID,
		identityID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id) VALUES (?::uuid)`,
		identityID,
	).Error)
}

func TestSyncAccountEmailCandidateProjectionDoesNotMutateIdentityTraits(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	seedAccountEmailProjectionPair(t, db, identityID, memberID, "delivery@example.test")
	require.NoError(t, db.Exec(`INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails) VALUES (?::uuid, ?::uuid, 'Active member', TRUE, 'delivery@example.test', ARRAY['delivery@example.test']::text[])`, memberID, identityID).Error)
	identity := &auth.Identity{
		ID:         identityID,
		ExternalID: memberID,
		Traits: structured.Fields{
			"email": "delivery@example.test",
		},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Via: "email", Value: "delivery@example.test", Verified: true},
		},
	}
	credentials := map[string]auth.Credential{
		"oidc": {
			Type:        "oidc",
			Identifiers: []string{"google:google-subject", "github:github-subject"},
			Config: structured.Fields{
				"providers": structured.Values{
					structured.Fields{
						"provider":       "google",
						"subject":        "google-subject",
						"email":          " Google@Example.Test ",
						"email_verified": true,
					},
					structured.Fields{
						"provider":               "github",
						"subject":                "github-subject",
						"primary_verified_email": "GitHub@Example.Test",
						"email":                  "ignored-unverified-github@example.test",
						"verified":               false,
						"primary":                true,
					},
				},
			},
		},
		"code": {
			Type:        "code",
			Identifiers: []string{"delivery@example.test"},
		},
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(t.Context(), credentials)
	identity.Credentials = credentials
	emailService := NewAccountEmailService(db, staticAccountEmailIdentity{identity}, memberEmailProjectionIntegration{})
	require.NoError(t, emailService.EnsureMemberPrimaryEmailUsable(
		t.Context(), identity.ID, identity, providerCandidates,
	))
	_, err := emailService.SyncMemberEmailProjection(
		t.Context(), identity.ID, identity, providerCandidates,
	)
	require.NoError(t, err)

	require.Equal(t, structured.Fields{"email": "delivery@example.test"}, identity.Traits)
	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Equal(t, "delivery@example.test", *member.PrimaryEmail)
	require.Equal(t, []string{"delivery@example.test", "github@example.test", "google@example.test"}, []string(member.AvailableEmails))
}
