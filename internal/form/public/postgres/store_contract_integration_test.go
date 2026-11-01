//go:build integration

package formpublicpostgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/form/public"
	formpublicpostgres "github.com/echovisionlab/geul-api/internal/form/public/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsThePublicFormStoreContractIntegration(t *testing.T) {
	t.Parallel()
	formtest.RunPublicStoreContract(t, func(t *testing.T) formtest.PublicHarness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The share link adapter is Share Link's own reader; media and
		// Member reads stay with their own tests.
		units := mutation.Postgres(db, func(tx *gorm.DB) public.Store {
			return formpublicpostgres.NewStore(tx, formpublicpostgres.Ports{ShareLinks: sharelinkadapter.FormLinks{}})
		})
		formUnits := mutation.Postgres(db, func(tx *gorm.DB) form.Store { return formpostgres.NewStore(tx, formpostgres.Ports{}) })
		return formtest.PublicHarness{
			Harness: mutationtest.PostgresHarness(db, units),
			Forms: formtest.Harness{
				Harness: mutationtest.PostgresHarness(db, formUnits),
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
				SeedSubmission: func(t *testing.T, submission form.Submission) form.Submission {
					require.NoError(t, formpostgres.InsertSubmission(context.Background(), db, &submission))
					return submission
				},
			},
			SeedShareLink: func(t *testing.T, link public.ShareLink) string {
				token := "token-" + uuid.NewString()[:16]
				require.NoError(t, db.Exec(`INSERT INTO share_link (token, entity_type, entity_id, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
					token, link.EntityType, link.EntityID, link.ExpiresAt, time.Now()).Error)
				return token
			},
		}
	})
}
