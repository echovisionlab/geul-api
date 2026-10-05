package mq

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestConsumerProcessPublishedCancellationResultSettlesInput(t *testing.T) {
	connection, mock := mockConnection(t)
	resultPublished := false
	consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		resultPublished = true
		return jobresult.Terminal(errors.New("explicit transcode cancellation result published"))
	})
	require.NoError(t, err)

	expectBoolean(mock, "delete", true)
	consumer.process(context.Background(), validMessage(t, 1))

	require.True(t, resultPublished)
	require.NoError(t, mock.ExpectationsWereMet(), "published cancellation must settle the input without requeue")
}

func TestConsumerProcessPublishedCancellationResultSettlesAcrossShutdown(t *testing.T) {
	connection, mock := mockConnection(t)
	parent, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	resultPublished := false
	consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		// Model a result publish that has succeeded just before the process
		// shutdown cancels the worker's parent context.
		resultPublished = true
		shutdown()
		return jobresult.Terminal(errors.New("explicit cancellation result published"))
	})
	require.NoError(t, err)

	expectBoolean(mock, "delete", true)
	consumer.process(parent, validMessage(t, 1))

	require.True(t, resultPublished)
	require.ErrorIs(t, parent.Err(), context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet(), "terminal result must settle the input without requeue after shutdown")
}

func TestConsumerProcessShutdownDuringTerminalPublicationFailureStillRequeues(t *testing.T) {
	connection, mock := mockConnection(t)
	parent, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		shutdown()
		return jobresult.Retry(errors.New("terminal result publication failed"))
	})
	require.NoError(t, err)

	expectRetry(mock, 0, nil)
	consumer.process(parent, validMessage(t, 1))

	require.NoError(t, mock.ExpectationsWereMet(), "failed terminal publication must remain redeliverable")
}

func TestTerminalResultSettlementContextIsDetachedAndBounded(t *testing.T) {
	type contextKey struct{}
	parent, shutdown := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "delivery"))
	shutdown()

	settleCtx, cancel := terminalResultSettlementContext(parent)
	defer cancel()
	require.NoError(t, settleCtx.Err())
	require.Equal(t, "delivery", settleCtx.Value(contextKey{}))
	deadline, ok := settleCtx.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), time.Duration(0))
	require.LessOrEqual(t, time.Until(deadline), terminalInputSettlementTimeout)
}

func TestConsumerProcessShutdownRedeliversFailedHandler(t *testing.T) {
	connection, mock := mockConnection(t)
	var handlerContextError error
	consumer, err := NewConsumer(connection, queueConfig(), func(ctx context.Context, _ []byte) error {
		handlerContextError = ctx.Err()
		return jobresult.Retry(errors.New("could not publish cancellation result during shutdown"))
	})
	require.NoError(t, err)

	expectRetry(mock, 0, nil)
	consumer.process(cancelledContext(), validMessage(t, 1))

	require.ErrorIs(t, handlerContextError, context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet())
}
