//go:build integration

package emailauthoringpostgres_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailauthoring/emailauthoringtest"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheEmailAuthoringStoreContractIntegration(t *testing.T) {
	t.Parallel()
	emailauthoringtest.RunStoreContract(t, func(t *testing.T) emailauthoringtest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The contract reaches the authoring tables, the content store and
		// Campaign's delivery history; Members, collaboration and rendering
		// stay with their own tests.
		ports := emailauthoringpostgres.Ports{
			ContentBlocks: contenttest.NewEmailContentBlockStore(t),
			References:    emailauthoringadapter.NewCampaignDeliveryReferences(),
		}
		units := mutation.Postgres(db, func(tx *gorm.DB) emailauthoring.Store { return emailauthoringpostgres.NewStore(tx, ports) })
		return mutationtest.PostgresHarness(db, units)
	})
}
