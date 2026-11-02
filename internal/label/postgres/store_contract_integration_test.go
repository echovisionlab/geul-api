//go:build integration

package labelpostgres_test

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/label/labeltest"
	labelpostgres "github.com/echovisionlab/geul-api/internal/label/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheLabelStoreContractIntegration(t *testing.T) {
	t.Parallel()
	labeltest.RunStoreContract(t, func(t *testing.T) labeltest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The contract reaches the label rows and the relation readers;
		// content, media and collaboration stay with their own tests.
		ports := labelpostgres.Ports{
			Artists: artistadapter.NewLabelRelations(), Releases: releaseadapter.NewLabelRelations(),
			ProgramEvents: programeventadapter.NewLabelRelations(),
		}
		units := mutation.Postgres(db, func(tx *gorm.DB) label.Store { return labelpostgres.NewStore(tx, ports) })
		return labeltest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedMember: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO member (id, nickname) VALUES (?, ?)`, id, "Contract "+id).Error)
				return id
			},
			SeedDocument: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, id).Error)
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
