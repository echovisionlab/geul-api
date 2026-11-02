//go:build integration

package emaildeliverypostgres_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/emaildelivery/emaildeliverytest"
	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	emaildeliverytest.RunStoreContract(t, func(t *testing.T) emaildeliverytest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return emaildeliverytest.Harness{
			Harness: mutationtest.PostgresHarness(db, emaildeliverypostgres.NewUnits(db)),
			SeedSiteSettings: func(t *testing.T, defaults emaildelivery.RenderDefaults) {
				result := db.Exec(`UPDATE site_settings
					SET site_title = ?, company_name = ?, legal_email = ?, contact_email = ?,
						logo_email_light_file_id = NULL, logo_email_dark_file_id = NULL
					WHERE id = 1`,
					defaults.SiteTitle, defaults.CompanyName, defaults.LegalEmail, defaults.ContactEmail)
				require.NoError(t, result.Error)
				require.EqualValues(t, 1, result.RowsAffected)
			},
		}
	})
}
