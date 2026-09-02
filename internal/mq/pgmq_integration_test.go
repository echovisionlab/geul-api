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
	queue := eventpkg.QueueAiMetadataGenerate
	pg := resetMQIntegrationQueues(t, queue)
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

func TestUserDeletionCommandsShareCallerTransactionIntegration(t *testing.T) {
	identityQueue := eventpkg.QueueUserDeleteIdentity
	avatarQueue := eventpkg.QueueUserDeleteAvatar
	pg := resetMQIntegrationQueues(t, identityQueue, avatarQueue)
	ctx := t.Context()
	publisher, err := NewPublisher(pg.SQLDB)
	require.NoError(t, err)

	memberID := uuid.NewString()
	identityID := uuid.NewString()
	avatarID := uuid.NewString()
	identityCommand := &managev1.UserDeleteIdentityCommand{
		Mode:     managev1.UserDeleteIdentityMode_TOMBSTONE,
		MemberId: memberID, IdentityId: identityID, AvatarAssetId: &avatarID,
	}
	avatarCommand := &managev1.UserDeleteAvatarCommand{MemberId: memberID, AvatarAssetId: &avatarID}

	rolledBack, err := pg.SQLDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, publisher.PublishUserDeleteIdentityWithExecutor(ctx, rolledBack, identityCommand))
	require.NoError(t, publisher.PublishUserDeleteAvatarWithExecutor(ctx, rolledBack, avatarCommand))
	require.NoError(t, rolledBack.Rollback())
	identityMessages, err := testutil.ReadPGMQ(ctx, pg.SQLDB, identityQueue, time.Minute, 1)
	require.NoError(t, err)
	avatarMessages, err := testutil.ReadPGMQ(ctx, pg.SQLDB, avatarQueue, time.Minute, 1)
	require.NoError(t, err)
	require.Empty(t, identityMessages)
	require.Empty(t, avatarMessages)

	committed, err := pg.SQLDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, publisher.PublishUserDeleteIdentityWithExecutor(ctx, committed, identityCommand))
	require.NoError(t, publisher.PublishUserDeleteAvatarWithExecutor(ctx, committed, avatarCommand))
	require.NoError(t, committed.Commit())
	identityMessages, err = testutil.ReadPGMQ(ctx, pg.SQLDB, identityQueue, time.Minute, 1)
	require.NoError(t, err)
	avatarMessages, err = testutil.ReadPGMQ(ctx, pg.SQLDB, avatarQueue, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, identityMessages, 1)
	require.Len(t, avatarMessages, 1)
	require.NoError(t, testutil.CompletePGMQ(ctx, pg.SQLDB, identityQueue, identityMessages[0].TransportID))
	require.NoError(t, testutil.CompletePGMQ(ctx, pg.SQLDB, avatarQueue, avatarMessages[0].TransportID))
}

func TestEnqueueProtobufParticipatesInCallerTransactionIntegration(t *testing.T) {
	queue := eventpkg.QueueAiMetadataGenerate
	pg := resetMQIntegrationQueues(t, queue)
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

func TestSharedPGMQReadPreservesContractInvalidTransportIDIntegration(t *testing.T) {
	queue := eventpkg.QueueAiMetadataGenerate
	pg := resetMQIntegrationQueues(t, queue)
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
