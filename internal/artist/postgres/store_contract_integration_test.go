//go:build integration

package artistpostgres_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/artist/artisttest"
	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheArtistStoreContractIntegration(t *testing.T) {
	t.Parallel()
	artisttest.RunStoreContract(t, func(t *testing.T) artisttest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The contract reaches the artist rows and the Label links; content,
		// media, collaboration and the other relation readers stay with
		// their own tests.
		ports := artistpostgres.Ports{Labels: labeladapter.NewArtistLinks()}
		units := mutation.Postgres(db, func(tx *gorm.DB) artist.Store { return artistpostgres.NewStore(tx, ports) })
		seedDocument := func(t *testing.T) string {
			id := uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, id).Error)
			return id
		}
		return artisttest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedMember: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO member (id, nickname) VALUES (?, ?)`, id, "Contract "+id).Error)
				return id
			},
			SeedDocument: seedDocument,
			SeedLabel: func(t *testing.T, title string) string {
				id := uuid.NewString()
				status := "LABEL_STATUS_PUBLISHED"
				require.NoError(t, db.Exec(`INSERT INTO label (id, status, source_locale, content_document_id) VALUES (?, ?, 'en', ?)`, id, status, seedDocument(t)).Error)
				require.NoError(t, db.Exec(`INSERT INTO label_translation (entity_id, locale, title, created_at, updated_at) VALUES (?, 'en', ?, now(), now())`, id, title).Error)
				return id
			},
		}
	})
}
