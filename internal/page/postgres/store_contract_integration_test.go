//go:build integration

package pagepostgres_test

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresStoreKeepsThePageStoreContractIntegration(t *testing.T) {
	t.Parallel()
	pagetest.RunStoreContract(t, func(t *testing.T) pagetest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The contract reaches the page tables; the content, media, Member,
		// Share Link and Menu runtimes stay with their own tests.
		units := mutation.Postgres(db, func(tx *gorm.DB) page.Store { return pagepostgres.NewStore(tx, pagepostgres.Ports{}) })
		return pagetest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedDocument: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'page')`, id).Error)
				return id
			},
			SeedFile: func(t *testing.T) string {
				id := uuid.NewString()
				digest := sha256.Sum256([]byte(id))
				require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, id, id, digest[:]).Error)
				return id
			},
		}
	})
}
