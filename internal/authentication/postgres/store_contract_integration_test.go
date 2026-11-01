//go:build integration

package authenticationpostgres_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authentication/authenticationtest"
	authenticationpostgres "github.com/echovisionlab/geul-api/internal/authentication/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	authenticationtest.RunStoreContract(t, func(t *testing.T) authenticationtest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		return authenticationtest.Harness{
			Harness: mutationtest.PostgresHarness(db, authenticationpostgres.NewUnits(db)),
			SeedMember: func(t *testing.T, state authenticationtest.Member) (string, string) {
				return seedMember(t, db, state)
			},
			SeedIdentity: func(t *testing.T) string {
				identityID := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, identityID).Error)
				return identityID
			},
		}
	})
}

// seedMember stores a Kratos Identity, its account Identity and a Member
// linked to both in state. A deleted Member is a tombstone, which the
// schema keeps unlinked.
func seedMember(t *testing.T, db *gorm.DB, state authenticationtest.Member) (identityID, memberID string) {
	t.Helper()
	identityID, memberID = uuid.NewString(), uuid.NewString()
	email := identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: email, Banned: state.Banned})
	now := testutil.DatabaseNow(t, db)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`, memberID, identityID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, identityID).Error; err != nil {
			return err
		}
		if state.Deleted {
			return tx.Exec(`
				INSERT INTO member (id, nickname, onboarded, created_at, updated_at, deleted_at)
				VALUES (?::uuid, 'deleted', ?, ?, ?, ?)`,
				memberID, state.Onboarded, now, now, now).Error
		}
		return tx.Exec(`
			INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails, created_at, updated_at)
			VALUES (?::uuid, ?::uuid, 'member', ?, ?, ARRAY[?], ?, ?)`,
			memberID, identityID, state.Onboarded, email, email, now, now).Error
	}))
	return identityID, memberID
}
