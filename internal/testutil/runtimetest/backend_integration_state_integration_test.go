//go:build integration

package runtimetest

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	orytest "github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestBackendIntegrationStacksIsolateDatabaseSpiceDBAndBucketsButShareKratos(t *testing.T) {
	t.Parallel()
	first, err := StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second, err := StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	require.NotEqual(t, first.Postgres.DatabaseName, second.Postgres.DatabaseName)
	require.NotEqual(t, first.SpiceDBToken, second.SpiceDBToken)
	require.NotEqual(t, first.S3MediaBucket, second.S3MediaBucket)
	require.NotEqual(t, first.S3CacheBucket, second.S3CacheBucket)

	var siteSettings int64
	require.NoError(t, second.Postgres.DB.Table("site_settings").Count(&siteSettings).Error)
	require.Positive(t, siteSettings, "clones carry schema bootstrap rows")

	categoryID := uuid.NewString()
	require.NoError(t, first.Postgres.DB.Exec(
		`INSERT INTO category (id, name, slug) VALUES (?::uuid, ?, ?)`,
		categoryID, "Isolated category", "isolated-"+categoryID,
	).Error)
	var visible int64
	require.NoError(t, second.Postgres.DB.Table("category").Where("id = ?::uuid", categoryID).Count(&visible).Error)
	require.Zero(t, visible, "committed rows stay in their own database")

	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(uuid.NewString()))
	require.NoError(t, err)
	_, err = first.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
	adminSubjects, err := second.SpiceDBClient.LookupGlobalSubjects(t.Context(), policyv1.Platform.LookupAdminSubjects())
	require.NoError(t, err)
	require.Empty(t, adminSubjects, "relationships stay in their own datastore")

	identity, err := first.KratosClient.CreateIdentity(t.Context(), &auth.Identity{
		SchemaID: "user",
		Traits:   structured.Fields{"email": "isolation-" + uuid.NewString() + "@example.test"},
	})
	require.NoError(t, err)
	var shared int64
	require.NoError(t, second.Postgres.DB.Table("kratos.identities").Where("id = ?::uuid", identity.ID).Count(&shared).Error)
	require.EqualValues(t, 1, shared, "every clone reads the shared Kratos tables")
}

func TestLocalKratosStackKeepsKratosRowsPrivate(t *testing.T) {
	t.Parallel()
	local := orytest.SetupOryStackWithLocalKratos(t)
	shared, err := StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, shared.Close()) })

	user := local.CreateUser(t, policyv1.Role.Author().ID())
	var localRows, sharedRows int64
	require.NoError(t, local.DB.Table("kratos.identities").Where("id = ?::uuid", user.IdentityID).Count(&localRows).Error)
	require.EqualValues(t, 1, localRows)
	require.NoError(t, shared.Postgres.DB.Table("kratos.identities").Where("id = ?::uuid", user.IdentityID).Count(&sharedRows).Error)
	require.Zero(t, sharedRows, "local Kratos rows never reach the shared Kratos database")

	var foreignTables int64
	require.NoError(t, local.DB.Raw(
		`SELECT count(*) FROM information_schema.foreign_tables WHERE foreign_table_schema = 'kratos'`,
	).Scan(&foreignTables).Error)
	require.Zero(t, foreignTables, "the local kratos schema holds real tables")
}

// Concurrent tests share Kratos. A FOR SHARE join from one test's members to
// kratos.identities must lock only that test's identities, or another test's
// identity update turns into a serialization failure under postgres_fdw's
// REPEATABLE READ remote transaction.
func TestSharedKratosJoinLocksOnlyJoinedIdentities(t *testing.T) {
	t.Parallel()
	first := orytest.SetupOryStack(t)
	second, err := StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	own := first.CreateUser(t, policyv1.Role.Author().ID())
	other, err := second.KratosClient.CreateIdentity(t.Context(), &auth.Identity{
		SchemaID: "user",
		Traits:   structured.Fields{"email": "join-lock-" + uuid.NewString() + "@example.test"},
	})
	require.NoError(t, err)

	err = first.DB.Transaction(func(tx *gorm.DB) error {
		var state string
		if err := tx.Raw(`SELECT state FROM kratos.identities WHERE id = ?::uuid`, own.IdentityID).Scan(&state).Error; err != nil {
			return err
		}
		if err := second.Postgres.DB.Exec(
			`UPDATE kratos.identities SET updated_at = NOW() WHERE id = ?::uuid`, other.ID,
		).Error; err != nil {
			return err
		}
		var memberIDs []string
		return tx.Raw(`
			SELECT CAST(member.id AS text)
			FROM member AS member
			JOIN kratos.identities AS identity
				ON identity.id = member.account_identity_id
				AND identity.external_id = CAST(member.id AS text)
				AND identity.state = 'active'
			WHERE member.id IN (?::uuid)
			FOR SHARE`, own.MemberID).Scan(&memberIDs).Error
	})
	require.NoError(t, err)
}
