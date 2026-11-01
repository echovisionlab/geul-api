//go:build integration

package postpostgres_test

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/post"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/post/posttest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresStoreKeepsThePostStoreContractIntegration(t *testing.T) {
	t.Parallel()
	posttest.RunStoreContract(t, func(t *testing.T) posttest.Harness {
		// Participant reads join the linked Identity; the Kratos stub keeps
		// the contract on PostgreSQL alone.
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		// The contract reaches the Post rows and the content revision; OG,
		// media, signals and collaboration stay with their own tests.
		deps := postpostgres.Deps{ContentBlocks: testutil.NewPostContentBlockStore(t)}
		units := mutation.Postgres(db, func(tx *gorm.DB) post.Store { return postpostgres.Bind(tx, deps) })
		insert := func(t *testing.T, query string, args ...any) {
			t.Helper()
			require.NoError(t, db.Exec(query, args...).Error)
		}
		return posttest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedDocument: func(t *testing.T) string {
				id := uuid.NewString()
				insert(t, `INSERT INTO content_document (id, profile) VALUES (?, 'post')`, id)
				return id
			},
			SeedMember: func(t *testing.T, nickname string) string {
				id := uuid.NewString()
				insert(t, `INSERT INTO member (id, nickname) VALUES (?, ?)`, id, nickname)
				return id
			},
			SeedTaxonomy: func(t *testing.T, taxonomy post.Taxonomy) string {
				id := uuid.NewString()
				insert(t, `INSERT INTO `+string(taxonomy)+` (id, name, slug) VALUES (?, ?, ?)`, id, id, id)
				return id
			},
			SeedFile: func(t *testing.T) string {
				id := uuid.NewString()
				digest := sha256.Sum256([]byte(id))
				insert(t, `INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, id, id, digest[:])
				return id
			},
			SeedMapPlace: func(t *testing.T) string {
				id := uuid.NewString()
				insert(t, `INSERT INTO map_place (id, name, address, lat, lng) VALUES (?, 'Contract place', 'Contract address', 0, 0)`, id)
				return id
			},
		}
	})
}
