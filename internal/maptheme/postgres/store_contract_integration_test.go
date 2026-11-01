//go:build integration

package postgres_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/maptheme/mapthemetest"
	mapthemepostgres "github.com/echovisionlab/geul-api/internal/maptheme/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheMapThemeStoreContractIntegration(t *testing.T) {
	t.Parallel()
	mapthemetest.RunStoreContract(t, func(t *testing.T) mapthemetest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		return mapthemetest.Harness{
			Harness: mutationtest.PostgresHarness(db, mapthemepostgres.NewUnits(db)),
			SeedContributor: func(t *testing.T) (string, string) {
				identityID := uuid.NewString()
				return orytest.SeedPostIntegrationIdentity(t, db, identityID, "Map Theme contributor"), identityID
			},
		}
	})
}
