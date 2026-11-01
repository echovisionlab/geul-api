//go:build integration

package formpostgres_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresStoreKeepsTheFormStoreContractIntegration(t *testing.T) {
	t.Parallel()
	formtest.RunStoreContract(t, func(t *testing.T) formtest.Harness {
		return postgresHarness(t, testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB)
	})
}

// postgresHarness runs the contract on the form tables and the content
// store; the media, OG, route, collaboration, Member and Share Link
// runtimes stay with their own tests.
func postgresHarness(t *testing.T, db *gorm.DB) formtest.Harness {
	t.Helper()
	contents, err := contentblock.NewGeneratedStore(noFileReuse{})
	require.NoError(t, err)
	units := mutation.Postgres(db, func(tx *gorm.DB) form.Store {
		return formpostgres.NewStore(tx, formpostgres.Ports{ContentBlocks: contents})
	})
	return formtest.Harness{
		Harness: mutationtest.PostgresHarness(db, units),
		SeedDocument: func(t *testing.T) string {
			id := uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, id).Error)
			return id
		},
		SeedMember: func(t *testing.T) string {
			id := uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO member (id, nickname) VALUES (?, ?)`, id, "Contract "+id).Error)
			return id
		},
		SeedFile: func(t *testing.T) string {
			id := uuid.NewString()
			digest := sha256.Sum256([]byte(id))
			require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, id, id, digest[:]).Error)
			return id
		},
		SeedSubmission: func(t *testing.T, submission form.Submission) form.Submission {
			require.NoError(t, formpostgres.InsertSubmission(t.Context(), db, &submission))
			return submission
		},
	}
}

type noFileReuse struct{}

func (noFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}
