//go:build integration

package filemediapostgres_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresLibraryStoreContractIntegration(t *testing.T) {
	t.Parallel()
	filemediatest.RunLibraryContract(t, func(t *testing.T) mutationtest.Harness[domain.Store] {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return mutationtest.PostgresHarness(db, filemediapostgres.NewUnits(db, filemediapostgres.Ports{}))
	})
}
