//go:build integration

package sitesettingspostgres_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sitesettings/sitesettingstest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresStoreKeepsTheSiteSettingsStoreContractIntegration(t *testing.T) {
	t.Parallel()
	sitesettingstest.RunStoreContract(t, func(t *testing.T) sitesettingstest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return sitesettingstest.Harness{
			Harness: mutationtest.PostgresHarness(db, newUnits(t, db)),
			SeedFile: func(t *testing.T) string {
				fileID := testutil.IntegrationUUID()
				require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension)
					VALUES (?, 'loader', 'image/webp', 1024, 'webp')`, fileID).Error)
				return fileID
			},
		}
	})
}
