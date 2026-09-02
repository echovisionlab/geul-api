//go:build integration

package transcode

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestTrackerPublishTranscodeAudioTracksJobAndSupersedesPreviousActiveJob(t *testing.T) {
	db := newTranscodeIntegrationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	postID := uuid.NewString()
	fileID := uuid.NewString()
	generationID := uuid.NewString()
	spectrogramAssetID := uuid.NewString()
	sourceKey, err := mediaauth.MediaObjectKey(fileID, "ogg")
	require.NoError(t, err)
	hlsPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	spectrogramKey, err := mediaauth.AssetObjectKey(spectrogramAssetID, "png")
	require.NoError(t, err)

	previousJob := model.TranscodeJob{
		EventID:    uuid.NewString(),
		QueueName:  eventpkg.QueueTranscoderAudio,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST.String(),
		EntityID:   postID,
		FileID:     fileID,
		Payload:    mustMarshalTrackerIntegrationProto(t, &managev1.TranscodeAudioEvent{EventId: "previous"}),
		Status:     StatusProcessing,
		CreatedAt:  now.Add(-2 * time.Minute),
		UpdatedAt:  now.Add(-time.Minute),
	}
	require.NoError(t, db.Create(&previousJob).Error)

	publisher := &trackerRecordingPublisher{}
	tracker := NewTracker(db, publisher)
	job := &managev1.TranscodeAudioEvent{
		EventId:    uuid.NewString(),
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   postID,
		FileId:     fileID,
		Source: &commonv1.MediaObjectTarget{
			FileId:    fileID,
			ObjectKey: sourceKey,
			Extension: "ogg",
			MimeType:  "audio/ogg",
		},
		HlsOutput: &commonv1.MediaGenerationWriteTarget{
			GenerationId: generationID,
			FileId:       fileID,
			ObjectPrefix: hlsPrefix,
		},
		SpectrogramOutput: &commonv1.AssetWriteTarget{
			AssetId:     spectrogramAssetID,
			ObjectKey:   spectrogramKey,
			Extension:   "png",
			MimeType:    "image/png",
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}

	require.NoError(t, tracker.PublishTranscodeAudio(ctx, job))
	require.NotEmpty(t, job.EventId)
	require.Len(t, publisher.audioEvents, 1)
	require.Equal(t, job.EventId, publisher.audioEvents[0].EventId)

	previous := loadTrackerIntegrationTranscodeJob(t, db, previousJob.EventID)
	require.Equal(t, StatusCancelled, previous.Status)
	require.NotNil(t, previous.LastError)
	require.Contains(t, *previous.LastError, "superseded")
	require.NotNil(t, previous.CompletedAt)

	current := loadTrackerIntegrationTranscodeJob(t, db, job.EventId)
	require.Equal(t, eventpkg.QueueTranscoderAudio, current.QueueName)
	require.Equal(t, StatusQueued, current.Status)
	require.Equal(t, 0, current.Progress)
	require.Equal(t, fileID, current.FileID)

	var saved managev1.TranscodeAudioEvent
	require.NoError(t, proto.Unmarshal(current.Payload, &saved))
	require.Equal(t, job.EventId, saved.EventId)
	require.Equal(t, job.GetSource().GetObjectKey(), saved.GetSource().GetObjectKey())
	require.Equal(t, generationID, saved.GetHlsOutput().GetGenerationId())
	require.Equal(t, spectrogramAssetID, saved.GetSpectrogramOutput().GetAssetId())

	failure := "late failure from an uncorrelated attempt"
	require.NoError(t, tracker.HandleTranscodeComplete(ctx, &managev1.TranscodeCompleteEvent{
		EventId:    uuid.NewString(),
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: job.EntityType, EntityId: postID, FileId: fileID, Error: &failure,
	}))
	require.Equal(t, StatusQueued, loadTrackerIntegrationTranscodeJob(t, db, job.EventId).Status)

	require.NoError(t, tracker.HandleTranscodeComplete(ctx, &managev1.TranscodeCompleteEvent{
		EventId:    job.EventId,
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: job.EntityType, EntityId: postID, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls:         &commonv1.MediaGenerationWriteResult{GenerationId: generationID},
			Spectrogram: &commonv1.AssetWriteResult{AssetId: spectrogramAssetID},
		},
	}))
	var deleted model.TranscodeJob
	require.ErrorIs(t, db.Where("event_id = ?", job.EventId).Take(&deleted).Error, gorm.ErrRecordNotFound)
	require.Equal(t, StatusCancelled, loadTrackerIntegrationTranscodeJob(t, db, previousJob.EventID).Status)
}

func TestTrackerIntegrationSchemaOmitsBrokerTransportColumns(t *testing.T) {
	db := newTranscodeIntegrationDB(t)

	var columns []string
	require.NoError(t, db.Raw(`
SELECT column_name
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'transcode_job'
  AND column_name IN ('attempts', 'last_heartbeat')
ORDER BY column_name
`).Scan(&columns).Error)
	require.Empty(t, columns)
}

func TestTrackerPublishTranscodeAudioIsIdempotentForStableEvent(t *testing.T) {
	db := newTranscodeIntegrationDB(t)
	ctx := context.Background()
	fileID := uuid.NewString()
	job := newTrackerIntegrationAudioJob(t, fileID)
	publisher := &trackerRecordingPublisher{}
	tracker := NewTracker(db, publisher)

	require.NoError(t, tracker.PublishTranscodeAudio(ctx, job))
	require.NoError(t, tracker.PublishTranscodeAudio(ctx, proto.Clone(job).(*managev1.TranscodeAudioEvent)))

	require.Len(t, publisher.audioEvents, 2)
	require.True(t, proto.Equal(publisher.audioEvents[0], publisher.audioEvents[1]))
	saved := loadTrackerIntegrationTranscodeJob(t, db, job.EventId)
	require.Equal(t, StatusQueued, saved.Status)

	conflict := proto.Clone(job).(*managev1.TranscodeAudioEvent)
	conflict.EntityId = uuid.NewString()
	require.ErrorContains(t, tracker.PublishTranscodeAudio(ctx, conflict), "conflicts with existing immutable payload")
	require.Len(t, publisher.audioEvents, 2)
}

func TestTrackerPublishTranscodeAudioRetriesSameStableCommandWithoutDatabaseTransportState(t *testing.T) {
	db := newTranscodeIntegrationDB(t)
	ctx := context.Background()
	job := newTrackerIntegrationAudioJob(t, uuid.NewString())
	publisher := &trackerRecordingPublisher{audioFailuresRemaining: 1}
	tracker := NewTracker(db, publisher)

	require.ErrorContains(t, tracker.PublishTranscodeAudio(ctx, job), "injected audio publish failure")
	queued := loadTrackerIntegrationTranscodeJob(t, db, job.EventId)
	require.Equal(t, StatusQueued, queued.Status)
	require.Nil(t, queued.LastError)

	require.NoError(t, tracker.PublishTranscodeAudio(ctx, proto.Clone(job).(*managev1.TranscodeAudioEvent)))
	require.Len(t, publisher.audioEvents, 1)
	require.True(t, proto.Equal(job, publisher.audioEvents[0]))
}

func TestTrackerRegisterTranscodeAudioUsesCallersTransaction(t *testing.T) {
	db := newTranscodeIntegrationDB(t)
	ctx := context.Background()
	job := newTrackerIntegrationAudioJob(t, uuid.NewString())
	job.EntityType = managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	job.EntityId = job.FileId
	tracker := NewTracker(db, &trackerRecordingPublisher{})

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tracker.RegisterTranscodeAudio(ctx, tx, job))
		return errors.New("rollback File ingest allocation and PGMQ enqueue")
	})
	require.ErrorContains(t, err, "rollback File ingest allocation")

	var saved model.TranscodeJob
	require.ErrorIs(t, db.Where("event_id = ?", job.EventId).Take(&saved).Error, gorm.ErrRecordNotFound)
}

func TestTrackerRegisterTranscodeVideoUsesCallersTransaction(t *testing.T) {
	db := newTranscodeIntegrationDB(t)
	ctx := context.Background()
	job := newTrackerIntegrationVideoJob(t, uuid.NewString())
	job.EntityType = managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	job.EntityId = job.FileId
	tracker := NewTracker(db, &trackerRecordingPublisher{})

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tracker.RegisterTranscodeVideo(ctx, tx, job))
		return errors.New("rollback File ingest allocation and PGMQ enqueue")
	})
	require.ErrorContains(t, err, "rollback File ingest allocation")

	var saved model.TranscodeJob
	require.ErrorIs(t, db.Where("event_id = ?", job.EventId).Take(&saved).Error, gorm.ErrRecordNotFound)
}

func newTrackerIntegrationAudioJob(t *testing.T, fileID string) *managev1.TranscodeAudioEvent {
	t.Helper()
	generationID := uuid.NewString()
	spectrogramAssetID := uuid.NewString()
	sourceKey, err := mediaauth.MediaObjectKey(fileID, "ogg")
	require.NoError(t, err)
	hlsPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	spectrogramKey, err := mediaauth.AssetObjectKey(spectrogramAssetID, "png")
	require.NoError(t, err)
	return &managev1.TranscodeAudioEvent{
		EventId:    uuid.NewString(),
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   uuid.NewString(),
		FileId:     fileID,
		Source: &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: sourceKey, Extension: "ogg", MimeType: "audio/ogg",
		},
		HlsOutput: &commonv1.MediaGenerationWriteTarget{
			GenerationId: generationID, FileId: fileID, ObjectPrefix: hlsPrefix,
		},
		SpectrogramOutput: &commonv1.AssetWriteTarget{
			AssetId: spectrogramAssetID, ObjectKey: spectrogramKey, Extension: "png",
			MimeType: "image/png", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

func newTrackerIntegrationVideoJob(t *testing.T, fileID string) *managev1.TranscodeVideoEvent {
	t.Helper()
	generationID := uuid.NewString()
	thumbnailAssetID := uuid.NewString()
	sourceKey, err := mediaauth.MediaObjectKey(fileID, "mp4")
	require.NoError(t, err)
	hlsPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	thumbnailKey, err := mediaauth.AssetObjectKey(thumbnailAssetID, "webp")
	require.NoError(t, err)
	return &managev1.TranscodeVideoEvent{
		EventId: uuid.NewString(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: uuid.NewString(), FileId: fileID,
		Source: &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: sourceKey, Extension: "mp4", MimeType: "video/mp4",
		},
		HlsOutput: &commonv1.MediaGenerationWriteTarget{
			GenerationId: generationID, FileId: fileID, ObjectPrefix: hlsPrefix,
		},
		ThumbnailOutput: &commonv1.AssetWriteTarget{
			AssetId: thumbnailAssetID, ObjectKey: thumbnailKey, Extension: "webp",
			MimeType: "image/webp", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

type trackerRecordingPublisher struct {
	audioEvents            []*managev1.TranscodeAudioEvent
	audioFailuresRemaining int
	videoEvents            []*managev1.TranscodeVideoEvent
	waveformCancelEvents   []*managev1.WaveformCancelEvent
	transcodeCancelEvents  []*managev1.TranscodeCancelEvent
}

func (p *trackerRecordingPublisher) PublishTranscodeAudio(_ context.Context, event *managev1.TranscodeAudioEvent) error {
	if p.audioFailuresRemaining > 0 {
		p.audioFailuresRemaining--
		return fmt.Errorf("injected audio publish failure")
	}
	p.audioEvents = append(p.audioEvents, event)
	return nil
}

func (p *trackerRecordingPublisher) PublishTranscodeVideo(_ context.Context, event *managev1.TranscodeVideoEvent) error {
	p.videoEvents = append(p.videoEvents, event)
	return nil
}

func (p *trackerRecordingPublisher) PublishWaveformCancel(_ context.Context, event *managev1.WaveformCancelEvent) error {
	p.waveformCancelEvents = append(p.waveformCancelEvents, event)
	return nil
}

func (p *trackerRecordingPublisher) PublishTranscodeCancel(_ context.Context, event *managev1.TranscodeCancelEvent) error {
	p.transcodeCancelEvents = append(p.transcodeCancelEvents, event)
	return nil
}

func loadTrackerIntegrationTranscodeJob(t *testing.T, db *gorm.DB, eventID string) model.TranscodeJob {
	t.Helper()

	var job model.TranscodeJob
	require.NoError(t, db.Where("event_id = ?", eventID).Take(&job).Error)
	return job
}

func mustMarshalTrackerIntegrationProto(t *testing.T, message proto.Message) []byte {
	t.Helper()

	payload, err := proto.Marshal(message)
	require.NoError(t, err)
	return payload
}
