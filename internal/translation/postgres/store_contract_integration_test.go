//go:build integration

package translationpostgres_test

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
	"github.com/echovisionlab/geul-api/internal/translation/translationtest"
)

func TestPostgresStoreKeepsTheTranslationStoreContractIntegration(t *testing.T) {
	t.Parallel()
	translationtest.RunStoreContract(t, func(t *testing.T) translationtest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		// The contract reaches only translation's own tables, so no domain
		// adapter or shared runtime is bound.
		units := mutation.Postgres(db, func(tx *gorm.DB) application.Store {
			return translationpostgres.Bind(tx, translationpostgres.Ports{})
		})
		return translationtest.Harness{
			Harness: mutationtest.PostgresHarness(db, units),
			SeedMember: func(t *testing.T) string {
				return testutil.SeedPostIntegrationIdentity(t, db, uuid.NewString(), "Translation requester")
			},
		}
	})
}
