package filemedia

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type recordingFileTranscoderPublisher struct {
	audioJobs           []*managev1.TranscodeAudioEvent
	videoJobs           []*managev1.TranscodeVideoEvent
	registeredAudioJobs []*managev1.TranscodeAudioEvent
	registeredVideoJobs []*managev1.TranscodeVideoEvent
	cancelEvents        []*managev1.TranscodeCancelEvent
	err                 error
}

func (p *recordingFileTranscoderPublisher) PublishTranscodeAudio(_ context.Context, job *managev1.TranscodeAudioEvent) error {
	p.audioJobs = append(p.audioJobs, job)
	return p.err
}

func (p *recordingFileTranscoderPublisher) PublishTranscodeVideo(_ context.Context, job *managev1.TranscodeVideoEvent) error {
	p.videoJobs = append(p.videoJobs, job)
	return p.err
}

func (p *recordingFileTranscoderPublisher) PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error {
	return nil
}

func (p *recordingFileTranscoderPublisher) RegisterTranscodeAudio(_ context.Context, _ *gorm.DB, job *managev1.TranscodeAudioEvent) error {
	p.registeredAudioJobs = append(p.registeredAudioJobs, job)
	return nil
}

func (p *recordingFileTranscoderPublisher) RegisterTranscodeVideo(_ context.Context, _ *gorm.DB, job *managev1.TranscodeVideoEvent) error {
	p.registeredVideoJobs = append(p.registeredVideoJobs, job)
	return nil
}

func (p *recordingFileTranscoderPublisher) PublishTranscodeCancel(_ context.Context, event *managev1.TranscodeCancelEvent) error {
	p.cancelEvents = append(p.cancelEvents, event)
	return nil
}

func (p *recordingFileTranscoderPublisher) MarkCancelled(context.Context, string, managev1.TranscodeCancelReason) error {
	return nil
}

func (p *recordingFileTranscoderPublisher) EnqueueProtobuf(
	ctx context.Context,
	queue string,
	_ string,
	message proto.Message,
) error {
	switch queue {
	case eventpkg.QueueTranscoderAudio:
		job, ok := message.(*managev1.TranscodeAudioEvent)
		if !ok {
			return fmt.Errorf("audio job has type %T", message)
		}
		return p.PublishTranscodeAudio(ctx, job)
	case eventpkg.QueueTranscoderVideo:
		job, ok := message.(*managev1.TranscodeVideoEvent)
		if !ok {
			return fmt.Errorf("video job has type %T", message)
		}
		return p.PublishTranscodeVideo(ctx, job)
	default:
		return fmt.Errorf("unsupported transcode queue %q", queue)
	}
}

func (p *recordingFileTranscoderPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (p *recordingFileTranscoderPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional fixture executor is required")
	}
	return p.EnqueueProtobuf(ctx, queue, messageID, message)
}

var _ TransactionalAsyncPublisher = (*recordingFileTranscoderPublisher)(nil)

func TestTriggerFileScopedProcessingPublishesAudioWithoutManualOptions(t *testing.T) {
	t.Parallel()

	publisher := &recordingFileTranscoderPublisher{}
	db := newTranscodeTargetUnitDB(t)
	fileID := seedTranscodeTargetSource(t, db, "mp3", "audio/mpeg")
	svc := &FileService{db: db, publisher: publisher, asyncPublisher: publisher}
	err := svc.triggerFileScopedProcessingIfNeeded(context.Background(), fileID)
	require.NoError(t, err)

	if len(publisher.audioJobs) != 1 {
		t.Fatalf("audio jobs = %d, want 1", len(publisher.audioJobs))
	}
	if len(publisher.videoJobs) != 0 {
		t.Fatalf("video jobs = %d, want 0", len(publisher.videoJobs))
	}
	require.Len(t, publisher.registeredAudioJobs, 1)
	require.Same(t, publisher.registeredAudioJobs[0], publisher.audioJobs[0])

	job := publisher.audioJobs[0]
	if job.EventId == "" {
		t.Fatal("expected event id")
	}
	if job.EntityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE {
		t.Fatalf("entity type = %s", job.EntityType)
	}
	if job.EntityId != fileID || job.FileId != fileID || job.GetSource().GetObjectKey() != "media/"+fileID+".mp3" {
		t.Fatalf("unexpected audio job: %#v", job)
	}
	if job.GetHlsOutput().GetGenerationId() == "" || job.GetSpectrogramOutput().GetAssetId() == "" {
		t.Fatalf("expected audio output targets: %#v", job)
	}
	require.Nil(t, job.ProtoReflect().Descriptor().Fields().ByName("options"))
	payload, err := protojson.Marshal(job)
	require.NoError(t, err)
	require.NotContains(t, string(payload), `"options"`)
}

func TestTriggerFileScopedProcessingPublishesVideo(t *testing.T) {
	t.Parallel()

	publisher := &recordingFileTranscoderPublisher{}
	db := newTranscodeTargetUnitDB(t)
	fileID := seedTranscodeTargetSource(t, db, "mp4", "video/mp4")
	svc := &FileService{db: db, publisher: publisher, asyncPublisher: publisher}
	err := svc.triggerFileScopedProcessingIfNeeded(context.Background(), fileID)
	require.NoError(t, err)

	if len(publisher.videoJobs) != 1 {
		t.Fatalf("video jobs = %d, want 1", len(publisher.videoJobs))
	}
	if len(publisher.audioJobs) != 0 {
		t.Fatalf("audio jobs = %d, want 0", len(publisher.audioJobs))
	}
	require.Len(t, publisher.registeredVideoJobs, 1)
	require.Same(t, publisher.registeredVideoJobs[0], publisher.videoJobs[0])

	job := publisher.videoJobs[0]
	if job.EventId == "" {
		t.Fatal("expected event id")
	}
	if job.EntityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE {
		t.Fatalf("entity type = %s", job.EntityType)
	}
	if job.EntityId != fileID || job.FileId != fileID || job.GetSource().GetObjectKey() != "media/"+fileID+".mp4" {
		t.Fatalf("unexpected video job: %#v", job)
	}
	if job.GetHlsOutput().GetGenerationId() == "" || job.GetThumbnailOutput().GetAssetId() == "" {
		t.Fatalf("expected video output targets: %#v", job)
	}
}

func TestTriggerFileScopedProcessingRejectsPendingDelete(t *testing.T) {
	t.Parallel()

	publisher := &recordingFileTranscoderPublisher{}
	db := newTranscodeTargetUnitDB(t)
	fileID := seedTranscodeTargetSource(t, db, "mp3", "audio/mpeg")
	svc := &FileService{db: db, publisher: publisher, asyncPublisher: publisher}
	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.File{}).Where("id = ?", fileID).Update("delete_requested_at", now).Error)
	err := svc.triggerFileScopedProcessingIfNeeded(context.Background(), fileID)
	require.Error(t, err)

	if len(publisher.audioJobs) != 0 || len(publisher.videoJobs) != 0 {
		t.Fatalf("expected no jobs for invalid entity type, got audio=%d video=%d", len(publisher.audioJobs), len(publisher.videoJobs))
	}
}

func TestTriggerFileScopedProcessingRetriesSameStableCommandAfterPublishError(t *testing.T) {
	t.Parallel()

	publisher := &recordingFileTranscoderPublisher{err: errors.New("confirm unavailable")}
	db := newTranscodeTargetUnitDB(t)
	fileID := seedTranscodeTargetSource(t, db, "mp3", "audio/mpeg")
	svc := &FileService{db: db, publisher: publisher, asyncPublisher: publisher}
	err := svc.triggerFileScopedProcessingIfNeeded(context.Background(), fileID)
	require.ErrorContains(t, err, "enqueue File audio transcode job")

	publisher.err = nil
	require.NoError(t, svc.triggerFileScopedProcessingIfNeeded(context.Background(), fileID))
	require.Len(t, publisher.audioJobs, 2)
	require.Len(t, publisher.registeredAudioJobs, 2)
	require.Equal(t, publisher.audioJobs[0].GetEventId(), publisher.audioJobs[1].GetEventId())
	require.Equal(t, publisher.registeredAudioJobs[0].GetEventId(), publisher.registeredAudioJobs[1].GetEventId())
	require.Equal(
		t,
		publisher.audioJobs[0].GetHlsOutput().GetGenerationId(),
		publisher.audioJobs[1].GetHlsOutput().GetGenerationId(),
	)
	require.Equal(
		t,
		publisher.audioJobs[0].GetSpectrogramOutput().GetAssetId(),
		publisher.audioJobs[1].GetSpectrogramOutput().GetAssetId(),
	)
}

func TestFileIngestAudioOutputAllocatorRejectsPendingSourceFile(t *testing.T) {
	t.Parallel()
	db := newTranscodeTargetUnitDB(t)
	fileID := seedTranscodeTargetSource(t, db, "mp3", "audio/mpeg")
	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.File{}).
		Where("id = ?", fileID).
		Update("delete_requested_at", now).Error)

	_, _, _, err := ensureStableFileIngestAudioOutputs(t.Context(), db, fileID)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	var assetCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Count(&assetCount).Error)
	require.Zero(t, assetCount)
	var generationCount int64
	require.NoError(t, db.Model(&model.MediaGeneration{}).Count(&generationCount).Error)
	require.Zero(t, generationCount)
}

func newTranscodeTargetUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newServiceUnitDB(t)
}

func seedTranscodeTargetSource(t *testing.T, db *gorm.DB, extension, mimeType string) string {
	t.Helper()
	fileID := uuid.NewString()
	require.NoError(t, db.Create(&model.File{
		ID:        fileID,
		FileName:  "source." + extension,
		MimeType:  mimeType,
		FileSize:  100,
		Extension: extension,
		CreatedAt: time.Now().UTC(),
	}).Error)
	return fileID
}
