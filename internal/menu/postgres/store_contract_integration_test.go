//go:build integration

package menupostgres_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/menu/menutest"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheMenuStoreContractIntegration(t *testing.T) {
	t.Parallel()
	menutest.RunStoreContract(t, func(t *testing.T) mutationtest.Harness[menu.Store] {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return mutationtest.PostgresHarness(db, menupostgres.NewUnits(db, menupostgres.Ports{}))
	})
}
