package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

type candidateMemberProjection struct{}

func (candidateMemberProjection) PrimaryEmail(ctx context.Context, db *gorm.DB, memberID, identityID string) (string, error) {
	var email string
	err := db.WithContext(ctx).Table("member").Where("id = ? AND account_identity_id = ?", memberID, identityID).Select("primary_email").Take(&email).Error
	return email, err
}

func (candidateMemberProjection) SyncEmailProjection(ctx context.Context, db *gorm.DB, memberID, identityID, primary string, available []string) error {
	return nil
}

func TestSyncAccountEmailCandidateProjectionDoesNotMutateIdentityTraits(t *testing.T) {
	identity := &auth.Identity{
		ID:         "identity-1",
		ExternalID: "member-1",
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
	db := newHookTestDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, primary_email)
		VALUES ('member-1', 'identity-1', 'delivery@example.test')
	`).Error)
	kratos := &hookIdentityManager{identity: identity}
	providerCandidates := account.ResolveAccountEmailProviderCandidates(t.Context(), credentials)
	identity.Credentials = credentials
	emailService := account.NewAccountEmailService(db, kratos, candidateMemberProjection{})
	require.NoError(t, emailService.EnsureMemberPrimaryEmailUsable(
		context.Background(), identity.ID, identity, providerCandidates,
	))
	_, err := emailService.SyncMemberEmailProjection(
		context.Background(), identity.ID, identity, providerCandidates,
	)
	require.NoError(t, err)

	require.Empty(t, kratos.updatedMetadata)
	require.Empty(t, kratos.updatedTraits)
	require.Equal(t, structured.Fields{"email": "delivery@example.test"}, identity.Traits)
}
