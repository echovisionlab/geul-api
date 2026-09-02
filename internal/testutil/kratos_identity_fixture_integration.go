//go:build integration

package testutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type KratosIdentityFixture struct {
	ID        string
	Email     string
	Name      string
	Locale    string
	State     string
	Banned    bool
	CreatedAt time.Time
	// EmailVerified defaults to true so DB-seeded identities match the normal
	// Kratos integration fixture shape unless a test explicitly opts out.
	EmailVerified *bool
}

func SeedKratosIdentityFixture(t *testing.T, db *gorm.DB, fixture KratosIdentityFixture) {
	t.Helper()
	EnsureKratosIdentityFixtureColumns(t, db)

	state := fixture.State
	if state == "" {
		state = auth.KratosStateActive
	}
	createdAt := fixture.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	email := fixture.Email
	if email == "" {
		email = fixture.ID + "@example.test"
	}

	args := structured.Values{
		fixture.ID,
		email,
		fixture.Name,
		fixture.Locale,
	}
	args = append(args,
		fixture.Banned,
		state,
		createdAt,
		createdAt,
	)

	require.NoError(t, db.Exec(
		`INSERT INTO kratos.identities (
			id,
			schema_id,
			traits,
			metadata_public,
			metadata_admin,
			state,
			created_at,
			updated_at
		)
		VALUES (
			?,
			'user',
			jsonb_build_object('email', ?::text, 'name', ?::text, 'preferred_locale', ?::text),
			'{}'::jsonb,
			jsonb_build_object('banned', ?::boolean),
			?,
			?,
			?
		)
		ON CONFLICT (id) DO UPDATE SET
			schema_id = EXCLUDED.schema_id,
			traits = EXCLUDED.traits,
			metadata_public = EXCLUDED.metadata_public,
			metadata_admin = EXCLUDED.metadata_admin,
			state = EXCLUDED.state,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at`,
		args...,
	).Error)

	emailVerified := true
	if fixture.EmailVerified != nil {
		emailVerified = *fixture.EmailVerified
	}
	SeedKratosVerifiableEmailFixture(t, db, fixture.ID, email, emailVerified, createdAt)
}

// ConstrainKratosIdentityAggregateFixture makes pre-existing shared Ory stack
// identities invisible to tests that assert exact global identity aggregates.
// Callers must use a transactional DB handle so this precondition rolls back
// with the test; regular tests should create unique identities instead.
func ConstrainKratosIdentityAggregateFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	EnsureKratosIdentityFixtureColumns(t, db)

	require.NoError(t, db.Exec(`
		UPDATE kratos.identities
		SET
			state = ?,
			metadata_admin = COALESCE(metadata_admin, '{}'::jsonb) || jsonb_build_object('banned', true),
			updated_at = NOW()
	`, auth.KratosStateInactive).Error)
}

func ResetFirstAdminBootstrapFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`DELETE FROM auth_bootstrap_state WHERE key = ?`, "first_admin").Error)
}

func EnsureKratosIdentityFixtureColumns(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS schema_id TEXT NOT NULL DEFAULT 'user'`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS traits JSONB NOT NULL DEFAULT '{}'::jsonb`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS metadata_public JSONB DEFAULT '{}'::jsonb`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS metadata_admin JSONB DEFAULT '{}'::jsonb`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'active'`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS nid UUID`).Error)
	require.NoError(t, db.Exec(`
		DO $$
		DECLARE
			network_id UUID;
		BEGIN
			IF to_regclass('kratos.networks') IS NOT NULL THEN
				EXECUTE 'SELECT id FROM kratos.networks LIMIT 1' INTO network_id;
			END IF;
			IF network_id IS NULL THEN
				network_id := gen_random_uuid();
			END IF;
			UPDATE kratos.identities SET nid = network_id WHERE nid IS NULL;
		END $$;
	`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS created_at TIMESTAMP NOT NULL DEFAULT NOW()`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identities ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP NOT NULL DEFAULT NOW()`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS kratos.identity_verifiable_addresses (
			id UUID PRIMARY KEY,
			status TEXT NOT NULL,
			via TEXT NOT NULL,
			verified BOOLEAN NOT NULL,
			value TEXT NOT NULL,
			verified_at TIMESTAMP NULL,
			identity_id UUID NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
			nid UUID NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE kratos.identity_verifiable_addresses ADD COLUMN IF NOT EXISTS nid UUID`).Error)
}

func SeedKratosVerifiableEmailFixture(t *testing.T, db *gorm.DB, identityID, email string, verified bool, now time.Time) {
	t.Helper()
	EnsureKratosIdentityFixtureColumns(t, db)
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var verifiedAt *time.Time
	status := "pending"
	if verified {
		verifiedAt = &now
		status = "completed"
	}

	require.NoError(t, db.Exec(
		`DELETE FROM kratos.identity_verifiable_addresses
		WHERE identity_id = ?::uuid
			AND via = 'email'
			AND lower(value) = lower(?::text)`,
		identityID,
		email,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO kratos.identity_verifiable_addresses (
			id,
			status,
			via,
			verified,
			value,
			verified_at,
			identity_id,
			created_at,
			updated_at,
			nid
		)
		SELECT
			gen_random_uuid(),
			?,
			'email',
			?,
			?,
			?,
			?::uuid,
			?,
			?,
			identities.nid
		FROM kratos.identities AS identities
		WHERE identities.id = ?::uuid`,
		status,
		verified,
		email,
		verifiedAt,
		identityID,
		now,
		now,
		identityID,
	).Error)
}
