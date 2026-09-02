package worker

import (
	"math"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestAuthEmailQueueRetryWindowFitsInsideCodeLifetime(t *testing.T) {
	config := authEmailQueueConfig()
	require.Equal(t, "api.manage.v1.SendEmailEvent", config.MessageType)
	retryWindow := time.Duration(0)
	for retry := 0; retry < config.MaxRetries; retry++ {
		retryWindow += time.Duration(
			float64(config.RetryDelay) * math.Pow(config.RetryBackoff, float64(retry)),
		)
	}

	require.Less(t, retryWindow, 15*time.Minute)
}

func TestCampaignEmailQueueHasDedicatedDurableRetryContract(t *testing.T) {
	config := campaignEmailQueueConfig()

	require.Equal(t, eventpkg.QueueEmailCampaign, config.Name)
	require.Equal(t, "api.manage.v1.SendBulkEmailBatchEvent", config.MessageType)
	require.Positive(t, config.Workers)
	require.Positive(t, config.MaxRetries)
	require.Positive(t, config.RetryDelay)
}

func TestDecodeCampaignEmailMessageUsesDirectBatchContract(t *testing.T) {
	body, err := proto.Marshal(&managev1.SendBulkEmailBatchEvent{
		DeliveryRunId: "delivery-run-1",
		BatchSize:     100,
		RatePerSecond: 10,
	})
	require.NoError(t, err)

	job, err := decodeCampaignEmailMessage(mq.Message{Body: body})
	require.NoError(t, err)
	require.Equal(t, "delivery-run-1", job.GetDeliveryRunId())
}

func TestDecodeCampaignEmailMessageRejectsEmptyPayload(t *testing.T) {
	_, err := decodeCampaignEmailMessage(mq.Message{})
	require.EqualError(t, err, "invalid campaign email job: delivery_run_id is required")
}
