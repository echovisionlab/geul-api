package worker

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestTranslationGenerateQueueUsesSingleSequentialWorkerAndTransportRecovery(t *testing.T) {
	config := translationGenerateQueueConfig()

	require.Equal(t, eventpkg.QueueTranslationGenerate, config.Name)
	require.Equal(t, "api.manage.v1.TranslationGenerateEvent", config.MessageType)
	require.Equal(t, 1, config.Workers)
	require.Zero(t, config.Timeout, "each provider call owns its timeout; bundle count does not have a fixed ceiling")
	require.Equal(t, 3, config.MaxRetries)
	require.Equal(t, time.Second, config.RetryDelay)
	require.Equal(t, float64(2), config.RetryBackoff)
}

func TestTranslationGenerateDeliveryPassesOnlyStableJobIdentity(t *testing.T) {
	processor := &recordingTranslationDeliveryProcessor{}
	handlers := &Handlers{translationJobs: processor}
	body, err := proto.Marshal(&managev1.TranslationGenerateEvent{JobId: "translation-job-1"})
	require.NoError(t, err)

	require.NoError(t, handlers.handleTranslationGenerateMessage(t.Context(), mq.Message{
		Body:        body,
		MessageID:   "translation-job-1",
		RetryCount:  2,
		Redelivered: true,
	}))
	require.Equal(t, "translation-job-1", processor.jobID)
}

func TestTranslationGenerateDeliveryRejectsMismatchedEnvelopeIdentityAsTerminal(t *testing.T) {
	processor := &recordingTranslationDeliveryProcessor{}
	handlers := &Handlers{translationJobs: processor}
	body, err := proto.Marshal(&managev1.TranslationGenerateEvent{JobId: "translation-job-1"})
	require.NoError(t, err)

	err = handlers.handleTranslationGenerateMessage(t.Context(), mq.Message{
		Body:      body,
		MessageID: "translation-job-2",
	})
	require.Error(t, err)
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "transport_payload_id_mismatch", class)
	require.Empty(t, processor.jobID, "mismatched delivery must not reach the domain processor")
}

type recordingTranslationDeliveryProcessor struct {
	jobID string
}

func (p *recordingTranslationDeliveryProcessor) ProcessDelivery(
	_ context.Context,
	jobID string,
) error {
	p.jobID = jobID
	return nil
}
