//go:build integration

package mq

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobs"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTranscodeResultAndInputCommitTogetherIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true})
	conn, err := NewConnection(pg.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	publisher, err := NewPublisher(conn)
	require.NoError(t, err)
	client := eventpkg.PGMQ{}
	config := jobs.DefaultAudioQueueConfig()

	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel_shutdown_commits", true: "missing_input_rolls_back_result"}[rollback], func(t *testing.T) {
			eventID := uuid.NewString()
			command := &apiv1.TranscodeAudioEvent{EventId: eventID, EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: uuid.NewString(), FileId: uuid.NewString()}
			body, err := proto.Marshal(command)
			require.NoError(t, err)
			envelope, err := eventpkg.NewEnvelope(eventID, config.MessageType, body)
			require.NoError(t, err)
			inputID, err := client.Enqueue(t.Context(), conn.DB(), config.Name, envelope, nil, 0)
			require.NoError(t, err)
			messages, err := client.Read(t.Context(), conn.DB(), config.Name, time.Minute, 1)
			require.NoError(t, err)
			require.Len(t, messages, 1)
			require.Equal(t, inputID, messages[0].TransportID)
			if rollback {
				require.NoError(t, client.Complete(t.Context(), conn.DB(), config.Name, inputID))
			}
			parent, stop := context.WithCancel(t.Context())
			defer stop()
			consumer, err := NewConsumer(conn, config, func(ctx context.Context, _ []byte) error {
				stop()
				settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				failure := "explicitly cancelled"
				if err := publisher.PublishComplete(settleCtx, &apiv1.TranscodeCompleteEvent{EventId: eventID, EventType: apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO, EntityType: command.EntityType, EntityId: command.EntityId, FileId: command.FileId, Error: &failure}); err != nil {
					return jobresult.Retry(err)
				}
				return jobresult.Terminal(errors.New(failure))
			})
			require.NoError(t, err)
			consumer.process(parent, messages[0])

			var inputs, results int
			require.NoError(t, conn.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM pgmq."q_transcoder.audio" WHERE msg_id = $1`, inputID).Scan(&inputs))
			require.NoError(t, conn.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM pgmq."q_transcode.result" WHERE message->>'message_id' = $1`, eventID).Scan(&results))
			require.Zero(t, inputs)
			if rollback {
				require.Zero(t, results, "a failed input deletion must roll back result enqueue")
			} else {
				require.Equal(t, 1, results, "shutdown must not retry or duplicate a committed result")
			}
		})
	}
}
