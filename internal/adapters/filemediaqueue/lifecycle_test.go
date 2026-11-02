package filemediaqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type lifecycleRecorder struct {
	calls                 []string
	messages              []proto.Message
	queue, id, signal     string
	enqueueErr, notifyErr error
}

func (p *lifecycleRecorder) EnqueueProtobuf(_ context.Context, queue, id string, message proto.Message) error {
	p.calls = append(p.calls, "enqueue")
	p.queue, p.id = queue, id
	p.messages = append(p.messages, message)
	return p.enqueueErr
}
func (p *lifecycleRecorder) NotifyProtobuf(_ context.Context, signal string, message proto.Message) error {
	p.calls = append(p.calls, "notify")
	p.signal = signal
	p.messages = append(p.messages, message)
	return p.notifyErr
}

func TestIngestLifecyclePreservesWirePayloadsAndOptionalValues(t *testing.T) {
	t.Parallel()
	input := domain.IngestEvent{CorrelationID: "correlation", Identity: &domain.IngestIdentity{EntityType: int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK), EntityID: "track", FileID: "file", Target: &domain.ObjectTarget{FileID: "file", ObjectKey: "media/file.ogg", Extension: "ogg", MimeType: "audio/ogg"}, Source: 999, MediaKind: int32(managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO), UploadID: new("upload"), SlotID: new(""), AttemptID: new("attempt"), ExpectedCurrentFileID: new("")}, SequenceNumber: 42, TimestampMS: 123, Progress: &domain.IngestProgress{Percentage: 100, BytesCompleted: new(int64(0)), BytesTotal: new(int64(100))}, FileName: "source.ogg", MimeType: "audio/ogg", FileSize: 100, Reason: 998, Error: "storage failed"}
	identity := &managev1.FileIngestIdentity{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, EntityId: "track", FileId: "file", Target: &commonv1.MediaObjectTarget{FileId: "file", ObjectKey: "media/file.ogg", Extension: "ogg", MimeType: "audio/ogg"}, Source: 999, MediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO, UploadId: new("upload"), SlotId: new(""), AttemptId: new("attempt"), ExpectedCurrentFileId: new("")}
	progress := &managev1.FileIngestProgress{Percentage: 100, BytesCompleted: new(int64(0)), BytesTotal: new(int64(100))}
	tests := []struct {
		kind domain.IngestEventKind
		want proto.Message
	}{
		{domain.IngestUploading, &managev1.FileIngestUploadEvent{CorrelationId: "correlation", Identity: identity, SequenceNumber: 42, TimestampMs: 123, Progress: progress}},
		{domain.IngestDownloading, &managev1.FileIngestDownloadEvent{CorrelationId: "correlation", Identity: identity, SequenceNumber: 42, TimestampMs: 123, Progress: progress}},
		{domain.IngestFinalized, &managev1.FileIngestFinalizedEvent{CorrelationId: "correlation", Identity: identity, SequenceNumber: 42, TimestampMs: 123, Progress: progress}},
		{domain.IngestAttached, &managev1.FileIngestAttachedEvent{CorrelationId: "correlation", Identity: identity, SequenceNumber: 42, TimestampMs: 123, FileName: "source.ogg", MimeType: "audio/ogg", FileSize: 100}},
		{domain.IngestFailed, &managev1.FileIngestFailedEvent{CorrelationId: "correlation", Identity: identity, SequenceNumber: 42, TimestampMs: 123, Progress: progress, Reason: 998, Error: "storage failed"}},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()
			publisher := &lifecycleRecorder{}
			event := input
			event.Kind = test.kind
			require.NoError(t, Lifecycle(publisher).PublishIngest(t.Context(), event))
			require.Equal(t, eventpkg.SignalFileIngest, publisher.signal)
			for _, actual := range publisher.messages {
				wantBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(test.want)
				require.NoError(t, err)
				actualBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(actual)
				require.NoError(t, err)
				require.Equal(t, test.want.ProtoReflect().Descriptor().FullName(), actual.ProtoReflect().Descriptor().FullName())
				require.Equal(t, wantBytes, actualBytes)
			}
			if test.kind == domain.IngestAttached {
				require.Equal(t, []string{"enqueue", "notify"}, publisher.calls)
				require.Equal(t, eventpkg.QueueReleaseTrackOriginalAudioProjection, publisher.queue)
				require.Equal(t, "file-ingest-attached:file", publisher.id)
			} else {
				require.Equal(t, []string{"notify"}, publisher.calls)
			}
		})
	}
	empty, err := ingestMessage(domain.IngestEvent{Kind: domain.IngestFailed})
	require.NoError(t, err)
	require.Nil(t, empty.(*managev1.FileIngestFailedEvent).Identity)
	require.Nil(t, empty.(*managev1.FileIngestFailedEvent).Progress)
}

func TestAttachedLifecycleFailureStopsBeforeNotification(t *testing.T) {
	t.Parallel()
	injected := errors.New("queue unavailable")
	publisher := &lifecycleRecorder{enqueueErr: injected}
	event := domain.IngestEvent{Kind: domain.IngestAttached, Identity: &domain.IngestIdentity{EntityType: int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK), MediaKind: int32(managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO), FileID: "file"}}
	require.ErrorIs(t, Lifecycle(publisher).PublishIngest(t.Context(), event), injected)
	require.Equal(t, []string{"enqueue"}, publisher.calls)
	publisher = &lifecycleRecorder{notifyErr: injected}
	require.ErrorIs(t, Lifecycle(publisher).PublishIngest(t.Context(), event), injected)
	require.Equal(t, []string{"enqueue", "notify"}, publisher.calls)
	publisher = &lifecycleRecorder{}
	event.Identity = nil
	require.EqualError(t, Lifecycle(publisher).PublishIngest(t.Context(), event), "file ingest attached identity is required")
	require.Empty(t, publisher.calls)
	require.EqualError(t, Lifecycle(nil).PublishIngest(t.Context(), domain.IngestEvent{Kind: domain.IngestUploading}), "async publisher is required")
	require.ErrorContains(t, Lifecycle(publisher).PublishIngest(t.Context(), domain.IngestEvent{Kind: "unknown"}), "unsupported file ingest event kind")
}

func TestProcessingLifecyclePreservesReadyOutputsAndOptionalIdentity(t *testing.T) {
	t.Parallel()
	publisher := &lifecycleRecorder{}
	input := domain.ProcessingEvent{CorrelationID: "correlation", EntityType: 999, EntityID: "track", FileID: "file", Status: int32(commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY), Outputs: &domain.ProcessingOutputs{HLSGenerationID: new("hls"), ThumbnailAssetID: new("thumbnail"), SpectrogramAssetID: new("spectrogram"), WaveformAssetID: new("waveform"), DurationSeconds: new(int32(0))}, TimestampMS: 123, SlotID: new(""), AttemptID: new("attempt"), TrackID: new("track"), ReleaseID: new("release")}
	require.NoError(t, Lifecycle(publisher).PublishProcessing(t.Context(), input))
	want := &managev1.MediaProcessingLifecycleEvent{CorrelationId: "correlation", EntityType: 999, EntityId: "track", FileId: "file", Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY, Outputs: &managev1.MediaProcessingLifecycleOutputs{HlsGenerationId: new("hls"), ThumbnailAssetId: new("thumbnail"), SpectrogramAssetId: new("spectrogram"), WaveformAssetId: new("waveform"), DurationSeconds: new(int32(0))}, TimestampMs: 123, SlotId: new(""), AttemptId: new("attempt"), TrackId: new("track"), ReleaseId: new("release")}
	require.Equal(t, []string{"notify"}, publisher.calls)
	require.Equal(t, eventpkg.SignalMediaProcessingLifecycle, publisher.signal)
	require.True(t, proto.Equal(want, publisher.messages[0]))
	require.NoError(t, Lifecycle(publisher).PublishProcessing(t.Context(), domain.ProcessingEvent{}))
	empty := publisher.messages[1].(*managev1.MediaProcessingLifecycleEvent)
	require.Nil(t, empty.Outputs)
	require.Nil(t, empty.SlotId)
	require.Nil(t, empty.AttemptId)
	require.Nil(t, empty.TrackId)
	require.Nil(t, empty.ReleaseId)
}
