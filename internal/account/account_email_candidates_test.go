package account

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"

	"github.com/stretchr/testify/require"

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
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(`CREATE TABLE member (
        id TEXT PRIMARY KEY, account_identity_id TEXT UNIQUE,
        primary_email TEXT, deleted_at DATETIME
    )`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, primary_email)
		VALUES ('member-1', 'identity-1', 'delivery@example.test')
	`).Error)
	// The Account service accepts a read-only identity port. A fake with no
	// mutation methods also verifies that synchronization needs no write capability.
	kratos := &fakeIdentityManager{identity: identity}
	providerCandidates := ResolveAccountEmailProviderCandidates(t.Context(), credentials)
	identity.Credentials = credentials
	emailService := NewAccountEmailService(db, kratos, candidateMemberProjection{})
	require.NoError(t, emailService.EnsureMemberPrimaryEmailUsable(
		context.Background(), identity.ID, identity, providerCandidates,
	))
	_, err = emailService.SyncMemberEmailProjection(
		context.Background(), identity.ID, identity, providerCandidates,
	)
	require.NoError(t, err)

	require.Equal(t, structured.Fields{"email": "delivery@example.test"}, identity.Traits)
}
