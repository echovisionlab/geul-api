//go:build integration

package programeventpostgres_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	"github.com/echovisionlab/geul-api/internal/programevent/programeventtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheProgramEventStoreContractIntegration(t *testing.T) {
	t.Parallel()
	programeventtest.RunStoreContract(t, func(t *testing.T) programeventtest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		contentBlocks, err := contentblock.NewGeneratedStore(noFileReuse{})
		require.NoError(t, err)
		// The contract reaches the program_event rows, their content
		// document and the Member count; media, sessions and signals stay
		// with their own tests.
		ports := programeventpostgres.Ports{ContentBlocks: contentBlocks, Members: memberadapter.ContentMembers{}}
		units := mutation.Postgres(db, func(tx *gorm.DB) programevent.Store {
			return programeventpostgres.Bind(tx, ports, nil).Store
		})
		return programeventtest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedRelated: func(t *testing.T, kind programevent.RelationKind) string {
				id := uuid.NewString()
				switch kind {
				case programevent.ClientRelation:
					require.NoError(t, db.Exec(`INSERT INTO client (id, name) VALUES (?, ?)`, id, "Client "+id).Error)
				default:
					documentID := uuid.NewString()
					require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, documentID).Error)
					table := "artist"
					if kind == programevent.LabelRelation {
						table = "label"
					}
					require.NoError(t, db.Exec(`INSERT INTO `+table+` (id, content_document_id) VALUES (?, ?)`, id, documentID).Error)
				}
				return id
			},
			SeedFile: func(t *testing.T) string {
				id := uuid.NewString()
				digest := sha256.Sum256([]byte(id))
				require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, id, id, digest[:]).Error)
				return id
			},
			SeedMember: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO member (id, nickname) VALUES (?, ?)`, id, "Contract "+id).Error)
				return id
			},
		}
	})
}

// noFileReuse admits no File reuse; the contract writes no File Block.
type noFileReuse struct{}

func (noFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}
