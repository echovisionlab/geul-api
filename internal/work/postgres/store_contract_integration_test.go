//go:build integration

package workpostgres_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	"github.com/echovisionlab/geul-api/internal/work/worktest"
)

func TestPostgresStoreKeepsTheWorkStoreContractIntegration(t *testing.T) {
	t.Parallel()
	worktest.RunStoreContract(t, func(t *testing.T) worktest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The contract reaches the work tables and the rows they reference;
		// the content, media, OG, collaboration and share link runtimes stay
		// with their own tests.
		units := mutation.Postgres(db, func(tx *gorm.DB) work.Store { return workpostgres.Bind(tx, workpostgres.Ports{}, nil).Store })
		return worktest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedDocument: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'work')`, id).Error)
				return id
			},
			SeedMember: func(t *testing.T, nickname string, preferredLocale *string, deleted bool) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO member (id, nickname, onboarded, preferred_locale, deleted_at)
					VALUES (?, ?, TRUE, ?, CASE WHEN ? THEN now() END)`, id, nickname, preferredLocale, deleted).Error)
				return id
			},
			SeedClient: func(t *testing.T, name string) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO client (id, name) VALUES (?, ?)`, id, name).Error)
				return id
			},
			SeedMapPlace: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO map_place (id, name, address, lat, lng) VALUES (?, 'Place', 'Address', 37.5, 127)`, id).Error)
				return id
			},
			SeedArtist: func(t *testing.T, name string, ownerMemberID string) string {
				id, documentID := uuid.NewString(), uuid.NewString()
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, documentID).Error; err != nil {
						return err
					}
					if err := tx.Exec(`INSERT INTO artist (id, content_document_id) VALUES (?, ?)`, id, documentID).Error; err != nil {
						return err
					}
					if err := tx.Exec(`INSERT INTO artist_translation (entity_id, locale, title) VALUES (?, 'en', ?)`, id, name).Error; err != nil {
						return err
					}
					if ownerMemberID == "" {
						return nil
					}
					return tx.Exec(`INSERT INTO artist_owner (artist_id, member_id) VALUES (?, ?)`, id, ownerMemberID).Error
				}))
				return id
			},
		}
	})
}
