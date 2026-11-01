//go:build integration

package mq

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestEmptyQueueConsumerBecomesReadyIntegration(t *testing.T) {
	t.Parallel()
	queue := eventpkg.QueueAiMetadataGenerate
	pg := newMQIntegrationPostgres(t, queue)
	ctx, cancel := context.WithCancel(t.Context())
	consumer := NewQueueConsumer(pg.SQLDB, QueueConfig{
		Name:        queue,
		MessageType: "api.manage.v1.MetadataGenerationQueueEvent",
	}, func(context.Context, Message) error { return nil })
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- consumer.start(ctx, func(err error) { ready <- err })
	}()

	require.NoError(t, <-ready)
	cancel()
	require.NoError(t, <-done)
}

func TestEnqueueProtobufParticipatesInCallerTransactionIntegration(t *testing.T) {
	t.Parallel()
	queue := eventpkg.QueueAiMetadataGenerate
	pg := newMQIntegrationPostgres(t, queue)
	ctx := t.Context()

	rolledBack, err := pg.SQLDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, EnqueueProtobuf(
		ctx,
		rolledBack,
		queue,
		"rollback-command",
		&managev1.MetadataGenerationQueueEvent{JobId: "rollback-job"},
	))
	require.NoError(t, rolledBack.Rollback())
	messages, err := testutil.ReadPGMQ(ctx, pg.SQLDB, queue, time.Minute, 1)
	require.NoError(t, err)
	require.Empty(t, messages)

	committed, err := pg.SQLDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, EnqueueProtobuf(
		ctx,
		committed,
		queue,
		"commit-command",
		&managev1.MetadataGenerationQueueEvent{JobId: "commit-job"},
	))
	require.NoError(t, committed.Commit())

	messages, err = testutil.ReadPGMQ(ctx, pg.SQLDB, queue, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "commit-command", messages[0].Envelope.MessageID)
	require.NoError(t, testutil.CompletePGMQ(ctx, pg.SQLDB, queue, messages[0].TransportID))
}

func TestPGMQStableMessageIDCanBeDetectedInTransportTableIntegration(t *testing.T) {
	t.Parallel()
	queue := eventpkg.QueueAiMetadataGenerate
	pg := newMQIntegrationPostgres(t, queue)
	ctx := t.Context()
	messageID := uuid.NewString()
	publisher, err := NewPublisher(pg.SQLDB)
	require.NoError(t, err)
	require.NoError(t, publisher.EnqueueProtobuf(
		ctx,
		queue,
		messageID,
		&managev1.MetadataGenerationQueueEvent{JobId: uuid.NewString()},
	))

	var pending bool
	err = pg.SQLDB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pgmq."q_ai.metadata.generate" WHERE message->>'message_id' = $1)`,
		messageID,
	).Scan(&pending)
	require.NoError(t, err)
	require.True(t, pending)
}

func TestSharedPGMQReadPreservesContractInvalidTransportIDIntegration(t *testing.T) {
	t.Parallel()
	queue := eventpkg.QueueAiMetadataGenerate
	pg := newMQIntegrationPostgres(t, queue)
	ctx := t.Context()

	var transportID int64
	require.NoError(t, pg.SQLDB.QueryRowContext(
		ctx,
		"SELECT pgmq.send($1, '{}'::jsonb, '{}'::jsonb, 0)",
		queue,
	).Scan(&transportID))

	messages, err := (eventpkg.PGMQ{}).Read(ctx, pg.SQLDB, queue, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, transportID, messages[0].TransportID)
	require.NotEmpty(t, messages[0].ContractError)
	require.NoError(t, (eventpkg.PGMQ{}).DeadLetter(ctx, pg.SQLDB, queue, transportID))

	messages, err = (eventpkg.PGMQ{}).Read(ctx, pg.SQLDB, queue, time.Minute, 1)
	require.NoError(t, err)
	require.Empty(t, messages)
}
