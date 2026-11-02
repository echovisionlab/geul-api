//go:build integration

package legalpostgres_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/legaltest"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheLegalStoreContractIntegration(t *testing.T) {
	t.Parallel()
	legaltest.RunStoreContract(t, func(t *testing.T) legaltest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The contract reaches the history and translation tables; the
		// content, OG, notice, Share Link and session runtimes stay with their
		// own tests.
		units := mutation.Postgres(db, func(tx *gorm.DB) legal.Store {
			return legalpostgres.Bind(tx, legalpostgres.Ports{}, nil).Store
		})
		return legaltest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedDocument: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'policy')`, id).Error)
				return id
			},
			Projection: func(t *testing.T, kind legal.PolicyKind, id, locale string) legal.Projection {
				var projection legal.Projection
				if locale == "" {
					require.NoError(t, db.Raw(`SELECT content AS html, content_text AS text, content_hash AS digest FROM `+string(kind)+`_history WHERE id = ? AND view_hash = content_hash`, id).Scan(&projection).Error)
					return projection
				}
				require.NoError(t, db.Raw(`SELECT content_html AS html, content_text AS text FROM `+string(kind)+`_translation WHERE entity_id = ? AND locale = ?`, id, locale).Scan(&projection).Error)
				return projection
			},
		}
	})
}
