package mq

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	transcodehandler "github.com/echovisionlab/geul-api/internal/transcoding/handler"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobs"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestConsumerPublishesCompleteAndDeletesInputInOneTransaction(t *testing.T) {
	connection, mock := mockConnection(t)
	publisher, err := NewPublisher(connection)
	require.NoError(t, err)
	job := validAudioTranscodeJob()
	consumer, err := NewConsumer(connection, transcodeAudioQueueConfig(), func(ctx context.Context, _ []byte) error {
		return publisher.PublishComplete(ctx, transcodeCompleteFor(job, true))
	})
	require.NoError(t, err)

	mock.ExpectBegin()
	expectEnqueue(mock, eventpkg.QueueTranscodeResult, 99, nil)
	expectBoolean(mock, "delete", true)
	mock.ExpectCommit()
	consumer.process(context.Background(), validAudioMessage(t, job))

	require.NoError(t, mock.ExpectationsWereMet(), "the committed transaction must also settle the input without a second delete")
}

func TestConsumerCancellationResultSettlementSurvivesOverlappingShutdown(t *testing.T) {
	connection, mock := mockConnection(t)
	publisher, err := NewPublisher(connection)
	require.NoError(t, err)
	parent, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	job := validAudioTranscodeJob()
	consumer, err := NewConsumer(connection, transcodeAudioQueueConfig(), func(ctx context.Context, _ []byte) error {
		resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalInputSettlementTimeout)
		defer cancel()
		if err := publisher.PublishComplete(resultCtx, transcodeCompleteFor(job, false)); err != nil {
			return jobresult.Retry(err)
		}
		shutdown()
		return jobresult.Terminal(errors.New("explicit cancellation result published"))
	})
	require.NoError(t, err)

	mock.ExpectBegin()
	expectEnqueue(mock, eventpkg.QueueTranscodeResult, 99, nil)
	expectBoolean(mock, "delete", true)
	mock.ExpectCommit()
	consumer.process(parent, validAudioMessage(t, job))

	require.ErrorIs(t, parent.Err(), context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet(), "the result transaction must own input deletion even when shutdown follows its commit")
}

func TestConsumerSuccessReceiptReplaySettlesInputWithResultTransaction(t *testing.T) {
	connection, mock := mockConnection(t)
	publisher, err := NewPublisher(connection)
	require.NoError(t, err)
	job := validAudioTranscodeJob()
	receipt := successfulAudioReceipt(job)
	receiptPayload, err := proto.Marshal(receipt)
	require.NoError(t, err)
	storage := &receiptReplayStorage{
		key:     job.GetHlsOutput().GetObjectPrefix() + "/master.m3u8",
		payload: receiptPayload,
	}
	processor, err := transcodehandler.NewHandler(transcodehandler.Options{
		JobTimeoutMinutes: 5,
		AudioHLSBitrate:   "128k",
		FFmpeg:            unusedFFmpegExecutor{},
		Storage:           storage,
		Publisher:         publisher,
		Admission:         activeAdmission{},
	})
	require.NoError(t, err)
	consumer, err := NewConsumer(connection, transcodeAudioQueueConfig(), DecodeAudioJob(processor.HandleAudioJob))
	require.NoError(t, err)
	message := validAudioMessage(t, job)

	mock.ExpectBegin()
	expectEnqueue(mock, eventpkg.QueueTranscodeResult, 99, nil)
	expectBoolean(mock, "delete", true)
	mock.ExpectCommit()
	consumer.process(context.Background(), message)

	require.Equal(t, 1, storage.completionChecks)
	require.NoError(t, mock.ExpectationsWereMet(), "receipt replay must enqueue the result and acknowledge input in one transaction")
}

func TestConsumerRejectsTranscodeEnvelopeIdentityMismatchBeforeHandling(t *testing.T) {
	connection, mock := mockConnection(t)
	job := validAudioTranscodeJob()
	handlerCalled := false
	consumer, err := NewConsumer(connection, transcodeAudioQueueConfig(), func(context.Context, []byte) error {
		handlerCalled = true
		return nil
	})
	require.NoError(t, err)
	message := validAudioMessage(t, job)
	message.Envelope.MessageID = uuid.NewString()

	expectBoolean(mock, "archive", true)
	consumer.process(context.Background(), message)

	require.False(t, handlerCalled, "identity mismatch must prevent media work")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerResultSettlementRetriesWhenEnqueueFails(t *testing.T) {
	cause := errors.New("result enqueue failed")
	runResultSettlementFailure(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectBegin()
		expectEnqueue(mock, eventpkg.QueueTranscodeResult, 0, cause)
		mock.ExpectRollback()
	})
}

func TestConsumerResultSettlementRetriesWhenInputDeleteFails(t *testing.T) {
	runResultSettlementFailure(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectBegin()
		expectEnqueue(mock, eventpkg.QueueTranscodeResult, 99, nil)
		expectBoolean(mock, "delete", false)
		mock.ExpectRollback()
	})
}

func TestConsumerResultSettlementRetriesWhenCommitIsUncertain(t *testing.T) {
	cause := errors.New("commit confirmation unavailable")
	runResultSettlementFailure(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectBegin()
		expectEnqueue(mock, eventpkg.QueueTranscodeResult, 99, nil)
		expectBoolean(mock, "delete", true)
		mock.ExpectCommit().WillReturnError(cause)
	})
}

func TestPublisherRejectsResultOutsideDeliveryIdentityOrDatabase(t *testing.T) {
	t.Run("tuple mismatch", func(t *testing.T) {
		connection, mock := mockConnection(t)
		publisher, err := NewPublisher(connection)
		require.NoError(t, err)
		job := validAudioTranscodeJob()
		body, err := proto.Marshal(job)
		require.NoError(t, err)
		message := validAudioMessage(t, job)
		settlement := newDeliverySettlement(connection.DB(), eventpkg.PGMQ{}, eventpkg.QueueTranscoderAudio, message, body)
		ctx := withDeliverySettlement(context.Background(), settlement)
		result := transcodeCompleteFor(job, true)
		result.FileId = uuid.NewString()

		err = publisher.PublishComplete(ctx, result)

		require.ErrorContains(t, err, "does not match current")
		settlement.finish()
		require.NoError(t, mock.ExpectationsWereMet(), "a mismatched tuple must not enqueue or delete input")
	})

	t.Run("different publisher database", func(t *testing.T) {
		deliveryConnection, deliveryMock := mockConnection(t)
		publisherConnection, publisherMock := mockConnection(t)
		publisher, err := NewPublisher(publisherConnection)
		require.NoError(t, err)
		job := validAudioTranscodeJob()
		body, err := proto.Marshal(job)
		require.NoError(t, err)
		message := validAudioMessage(t, job)
		settlement := newDeliverySettlement(deliveryConnection.DB(), eventpkg.PGMQ{}, eventpkg.QueueTranscoderAudio, message, body)
		ctx := withDeliverySettlement(context.Background(), settlement)

		err = publisher.PublishComplete(ctx, transcodeCompleteFor(job, true))

		require.ErrorContains(t, err, "current delivery database")
		settlement.finish()
		require.NoError(t, deliveryMock.ExpectationsWereMet())
		require.NoError(t, publisherMock.ExpectationsWereMet())
	})
}

func TestWaveformDeliveryCannotSettleThroughTranscodeCompletionPublisher(t *testing.T) {
	connection, mock := mockConnection(t)
	publisher, err := NewPublisher(connection)
	require.NoError(t, err)
	config := queueConfig()
	config.Name = eventpkg.QueueWaveformGenerate
	config.MessageType = "api.manage.v1.WaveformGenerateEvent"
	consumer, err := NewConsumer(connection, config, func(ctx context.Context, _ []byte) error {
		err := publisher.PublishComplete(ctx, &apiv1.TranscodeCompleteEvent{
			EventId: "waveform-command", EventType: apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		})
		return jobresult.Retry(err)
	})
	require.NoError(t, err)
	envelope, err := eventpkg.NewEnvelope("waveform-command", config.MessageType, []byte("waveform"))
	require.NoError(t, err)

	expectRetryForQueue(mock, eventpkg.QueueWaveformGenerate, 5, nil)
	consumer.process(context.Background(), eventpkg.Message{TransportID: 42, ReadCount: 1, Envelope: envelope})

	require.NoError(t, mock.ExpectationsWereMet(), "waveform completion misuse must not delete the waveform input")
}

func runResultSettlementFailure(t *testing.T, expectTransaction func(sqlmock.Sqlmock)) {
	t.Helper()
	connection, mock := mockConnection(t)
	publisher, err := NewPublisher(connection)
	require.NoError(t, err)
	job := validAudioTranscodeJob()
	var publishErr error
	var handlerErr error
	consumer, err := NewConsumer(connection, transcodeAudioQueueConfig(), func(ctx context.Context, _ []byte) error {
		publishErr = publisher.PublishComplete(ctx, transcodeCompleteFor(job, false))
		if publishErr != nil {
			handlerErr = jobresult.Retry(publishErr)
			return handlerErr
		}
		return nil
	})
	require.NoError(t, err)

	expectTransaction(mock)
	expectRetry(mock, 5, nil)
	consumer.process(context.Background(), validAudioMessage(t, job))

	require.Error(t, publishErr)
	require.True(t, jobresult.IsRetry(handlerErr), "a settlement failure must never be reported as success")
	require.NoError(t, mock.ExpectationsWereMet())
}

type activeAdmission struct{}

func (activeAdmission) Admit(context.Context, transcodehandler.JobIdentity) (transcodehandler.JobAdmissionDecision, error) {
	return transcodehandler.JobAdmissionProceed, nil
}

type unusedFFmpegExecutor struct {
	transcodehandler.FFmpegExecutor
}

type receiptReplayStorage struct {
	key              string
	payload          []byte
	completionChecks int
}

func (receiptReplayStorage) Download(context.Context, string, string) error {
	panic("receipt replay must not download media")
}

func (receiptReplayStorage) Upload(context.Context, string, string, string) error {
	panic("receipt replay must not upload media")
}

func (receiptReplayStorage) UploadCompleted(context.Context, string, string, string, []byte) error {
	panic("receipt replay must not upload media")
}

func (s *receiptReplayStorage) Completion(_ context.Context, key string) ([]byte, bool, error) {
	s.completionChecks++
	return append([]byte(nil), s.payload...), key == s.key, nil
}

func validAudioTranscodeJob() *apiv1.TranscodeAudioEvent {
	eventID := uuid.NewString()
	entityID := uuid.NewString()
	fileID := uuid.NewString()
	generationID := uuid.NewString()
	assetID := uuid.NewString()
	return &apiv1.TranscodeAudioEvent{
		EventId:    eventID,
		EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   entityID,
		FileId:     fileID,
		Source: &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: "media/" + fileID + ".mp3", Extension: "mp3", MimeType: "audio/mpeg",
		},
		HlsOutput: &commonv1.MediaGenerationWriteTarget{
			GenerationId: generationID, FileId: fileID, ObjectPrefix: "media/" + fileID + "/hls/" + generationID,
		},
		SpectrogramOutput: &commonv1.AssetWriteTarget{
			AssetId: assetID, ObjectKey: "asset/" + assetID + ".png", Extension: "png", MimeType: "image/png",
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

func successfulAudioReceipt(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeCompleteEvent {
	return &apiv1.TranscodeCompleteEvent{
		EventId:    job.GetEventId(),
		EventType:  apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: job.GetEntityType(),
		EntityId:   job.GetEntityId(),
		FileId:     job.GetFileId(),
		Success:    true,
		Outputs: &apiv1.TranscodeOutputs{
			Hls:         &commonv1.MediaGenerationWriteResult{GenerationId: job.GetHlsOutput().GetGenerationId()},
			Spectrogram: &commonv1.AssetWriteResult{AssetId: job.GetSpectrogramOutput().GetAssetId()},
		},
	}
}

func validAudioMessage(t *testing.T, job *apiv1.TranscodeAudioEvent) eventpkg.Message {
	t.Helper()
	body, err := proto.Marshal(job)
	require.NoError(t, err)
	envelope, err := eventpkg.NewEnvelope(job.GetEventId(), "api.manage.v1.TranscodeAudioEvent", body)
	require.NoError(t, err)
	return eventpkg.Message{TransportID: 42, ReadCount: 1, Envelope: envelope}
}

func transcodeAudioQueueConfig() jobs.QueueConfig {
	config := queueConfig()
	config.Name = eventpkg.QueueTranscoderAudio
	config.MessageType = "api.manage.v1.TranscodeAudioEvent"
	return config
}

func transcodeCompleteFor(job *apiv1.TranscodeAudioEvent, success bool) *apiv1.TranscodeCompleteEvent {
	return &apiv1.TranscodeCompleteEvent{
		EventId: job.GetEventId(), EventType: apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: job.GetEntityType(), EntityId: job.GetEntityId(), FileId: job.GetFileId(), Success: success,
	}
}

func expectRetryForQueue(mock sqlmock.Sqlmock, queue string, seconds int, err error) {
	expectation := mock.ExpectQuery(`SELECT msg_id FROM pgmq.set_vt\(\$1, \$2::bigint, \$3::integer\)`).
		WithArgs(queue, int64(42), seconds)
	if err != nil {
		expectation.WillReturnError(err)
		return
	}
	expectation.WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(42))
}
