package mq

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	"github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

func TestConsumerProcessInvalidContractReportsArchiveFailure(t *testing.T) {
	connection, mock := mockConnection(t)
	handlerCalled := false
	consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		handlerCalled = true
		return nil
	})
	require.NoError(t, err)

	expectBoolean(mock, "archive", false)
	consumer.process(context.Background(), event.Message{
		TransportID:   42,
		ContractError: "invalid envelope",
	})

	require.False(t, handlerCalled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerProcessInvalidPayloadArchivesWithoutHandling(t *testing.T) {
	connection, mock := mockConnection(t)
	handlerCalled := false
	consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		handlerCalled = true
		return nil
	})
	require.NoError(t, err)

	message := validMessage(t, 1)
	message.Envelope.PayloadBase64 = "not-base64"
	expectBoolean(mock, "archive", true)
	consumer.process(context.Background(), message)

	require.False(t, handlerCalled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerProcessTerminalCompletionFailureDoesNotRetry(t *testing.T) {
	connection, mock := mockConnection(t)
	consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		return jobresult.Terminal(errors.New("terminal result persisted"))
	})
	require.NoError(t, err)

	expectBoolean(mock, "delete", false)
	consumer.process(context.Background(), validMessage(t, 1))

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerProcessShutdownRequeuesCompletedHandler(t *testing.T) {
	connection, mock := mockConnection(t)
	var handlerContextError error
	consumer, err := NewConsumer(connection, queueConfig(), func(ctx context.Context, _ []byte) error {
		handlerContextError = ctx.Err()
		return nil
	})
	require.NoError(t, err)

	expectRetry(mock, 0, nil)
	consumer.process(cancelledContext(), validMessage(t, 1))

	require.ErrorIs(t, handlerContextError, context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet())
}
