//go:build integration

package campaignpostgres_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPostgresStoreKeepsTheCampaignStoreContractIntegration(t *testing.T) {
	t.Parallel()
	campaigntest.RunStoreContract(t, func(t *testing.T) campaigntest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true, BootstrapKratosStub: true}).DB
		sqlDB, err := db.DB()
		require.NoError(t, err)
		publisher, err := mq.NewPublisher(sqlDB)
		require.NoError(t, err)
		ports := campaignpostgres.Ports{Publisher: publisher, ContentBlocks: contenttest.NewEmailContentBlockStore(t)}
		units := mutation.Postgres(db, func(tx *gorm.DB) campaign.Store { return campaignpostgres.NewStore(tx, ports) })
		return campaigntest.Harness{Harness: mutationtest.PostgresHarness(db, units), QueueMessages: func(t *testing.T) []proto.Message {
			t.Helper()
			rows, err := pgtest.ReadPGMQ(t.Context(), sqlDB, eventpkg.QueueEmailSend, time.Minute, 10)
			require.NoError(t, err)
			result := make([]proto.Message, len(rows))
			for i, row := range rows {
				payload, err := row.Envelope.Payload()
				require.NoError(t, err)
				job := &managev1.SendEmailEvent{}
				require.NoError(t, proto.Unmarshal(payload, job))
				result[i] = job
			}
			return result
		}, SeedRecipient: func(t *testing.T, v campaign.DeliveryRecipient) campaign.DeliveryRecipient {
			t.Helper()
			identityID := uuid.NewString()
			memberID := orytest.SeedPostIntegrationIdentity(t, db, identityID, "Campaign contract "+identityID)
			v.MemberID, v.IdentityID = &memberID, &identityID
			row := campaignpostgres.DeliveryRecipientToRow(v)
			require.NoError(t, db.Create(&row).Error)
			return campaignpostgres.DeliveryRecipientFromRow(row)
		}}
	})
}
