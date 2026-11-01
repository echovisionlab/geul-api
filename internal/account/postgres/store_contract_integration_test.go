//go:build integration

package accountpostgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"

	// contractUnits binds the Account rows only; the contract reaches none of
	// the Member ports.
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

func contractUnits(db *gorm.DB) mutation.UnitOfWork[account.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) account.Store { return accountpostgres.Bind(tx, accountpostgres.Ports{}) })
}

// seedPair stores a Kratos stub Identity, its account anchor and a Member
// linked to it both ways whose only email is the Identity's.
func seedPair(t *testing.T, db *gorm.DB, onboarded bool) (string, string, string) {
	t.Helper()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	email := identityID + "@example.test"
	orytest.SeedKratosIdentityFixture(t, db, orytest.KratosIdentityFixture{ID: identityID, Email: email, Name: "member", CreatedAt: time.Now().UTC().Add(-time.Hour)})
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails, social_links)
		VALUES (?::uuid, ?::uuid, ?, ?, ?, ARRAY[?]::text[], '{}'::jsonb)
	`, memberID, identityID, "member-"+memberID[:8], onboarded, email, email).Error)
	return memberID, identityID, email
}

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	accounttest.RunStoreContract(t, func(t *testing.T) accounttest.Fixture {
		// The Kratos stub backs the bilateral Member and Identity link and
		// the session reads.
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		return accounttest.Fixture{
			Harness:  mutationtest.PostgresHarness(db, contractUnits(db)),
			SeedPair: func(t *testing.T, onboarded bool) (string, string, string) { return seedPair(t, db, onboarded) },
			SeedSession: func(t *testing.T, identityID string, authenticatedAt time.Time) string {
				sessionID := uuid.NewString()
				// The Kratos stub's sessions table has no authentication methods.
				require.NoError(t, db.Exec(`
					INSERT INTO kratos.sessions (id, identity_id, active, authenticated_at, expires_at, created_at, updated_at)
					VALUES (?::uuid, ?::uuid, TRUE, ?, ?, ?, ?)
				`, sessionID, identityID, authenticatedAt, authenticatedAt.Add(time.Hour), authenticatedAt, authenticatedAt).Error)
				return sessionID
			},
			SetIdentityState: func(t *testing.T, identityID, state string) {
				require.NoError(t, db.Exec("UPDATE kratos.identities SET state = ? WHERE id = ?::uuid", state, identityID).Error)
			},
		}
	})
}
