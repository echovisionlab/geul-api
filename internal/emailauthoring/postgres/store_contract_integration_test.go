//go:build integration

package emailauthoringpostgres_test

import (
	"testing"

	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailauthoring/emailauthoringtest"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresStoreKeepsTheEmailAuthoringStoreContractIntegration(t *testing.T) {
	t.Parallel()
	emailauthoringtest.RunStoreContract(t, func(t *testing.T) emailauthoringtest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		// The contract reaches the authoring tables, the content store and
		// Campaign's delivery history; Members, collaboration and rendering
		// stay with their own tests.
		ports := emailauthoringpostgres.Ports{
			ContentBlocks: testutil.NewEmailContentBlockStore(t, nil),
			References:    emailauthoringadapter.NewCampaignDeliveryReferences(),
		}
		units := mutation.Postgres(db, func(tx *gorm.DB) emailauthoring.Store { return emailauthoringpostgres.NewStore(tx, ports) })
		return mutationtest.PostgresHarness(db, units)
	})
}
