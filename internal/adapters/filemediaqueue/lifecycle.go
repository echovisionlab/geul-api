package filemediaqueue

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mq"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

type lifecycleTransport interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

type LifecyclePublisher struct{ publisher lifecycleTransport }

func Lifecycle(publisher lifecycleTransport) LifecyclePublisher { return LifecyclePublisher{publisher} }

func (p LifecyclePublisher) PublishIngest(ctx context.Context, input domain.IngestEvent) error {
	message, err := ingestMessage(input)
	if err != nil {
		return err
	}
	if err := mq.ValidateFileIngestSignal(message); err != nil {
		return err
	}
	if p.publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	if attached, ok := message.(*managev1.FileIngestAttachedEvent); ok {
		id, err := mq.FileIngestAttachedMessageID(attached)
		if err != nil {
			return err
		}
		if err := p.publisher.EnqueueProtobuf(ctx, eventpkg.QueueReleaseTrackOriginalAudioProjection, id, attached); err != nil {
			return err
		}
	}
	return p.publisher.NotifyProtobuf(ctx, eventpkg.SignalFileIngest, message)
}

func ingestMessage(v domain.IngestEvent) (proto.Message, error) {
	var identity *managev1.FileIngestIdentity
	if i := v.Identity; i != nil {
		var target *commonv1.MediaObjectTarget
		if t := i.Target; t != nil {
			target = &commonv1.MediaObjectTarget{FileId: t.FileID, ObjectKey: t.ObjectKey, Extension: t.Extension, MimeType: t.MimeType}
		}
		identity = &managev1.FileIngestIdentity{EntityType: managev1.TranscodeEntityType(i.EntityType), EntityId: i.EntityID, FileId: i.FileID, Target: target, Source: managev1.FileIngestSource(i.Source), MediaKind: managev1.FileIngestMediaKind(i.MediaKind), UploadId: i.UploadID, SlotId: i.SlotID, AttemptId: i.AttemptID, ExpectedCurrentFileId: i.ExpectedCurrentFileID}
	}
	var progress *managev1.FileIngestProgress
	if p := v.Progress; p != nil {
		progress = &managev1.FileIngestProgress{Percentage: p.Percentage, BytesCompleted: p.BytesCompleted, BytesTotal: p.BytesTotal}
	}
	switch v.Kind {
	case domain.IngestUploading:
		return &managev1.FileIngestUploadEvent{CorrelationId: v.CorrelationID, Identity: identity, SequenceNumber: v.SequenceNumber, TimestampMs: v.TimestampMS, Progress: progress}, nil
	case domain.IngestDownloading:
		return &managev1.FileIngestDownloadEvent{CorrelationId: v.CorrelationID, Identity: identity, SequenceNumber: v.SequenceNumber, TimestampMs: v.TimestampMS, Progress: progress}, nil
	case domain.IngestFinalized:
		return &managev1.FileIngestFinalizedEvent{CorrelationId: v.CorrelationID, Identity: identity, SequenceNumber: v.SequenceNumber, TimestampMs: v.TimestampMS, Progress: progress}, nil
	case domain.IngestAttached:
		return &managev1.FileIngestAttachedEvent{CorrelationId: v.CorrelationID, Identity: identity, SequenceNumber: v.SequenceNumber, TimestampMs: v.TimestampMS, FileName: v.FileName, MimeType: v.MimeType, FileSize: v.FileSize}, nil
	case domain.IngestFailed:
		return &managev1.FileIngestFailedEvent{CorrelationId: v.CorrelationID, Identity: identity, SequenceNumber: v.SequenceNumber, TimestampMs: v.TimestampMS, Reason: managev1.FileIngestFailureReason(v.Reason), Error: v.Error, Progress: progress}, nil
	default:
		return nil, fmt.Errorf("unsupported file ingest event kind %q", v.Kind)
	}
}

func (p LifecyclePublisher) PublishProcessing(ctx context.Context, v domain.ProcessingEvent) error {
	if p.publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	var outputs *managev1.MediaProcessingLifecycleOutputs
	if o := v.Outputs; o != nil {
		outputs = &managev1.MediaProcessingLifecycleOutputs{HlsGenerationId: o.HLSGenerationID, ThumbnailAssetId: o.ThumbnailAssetID, SpectrogramAssetId: o.SpectrogramAssetID, WaveformAssetId: o.WaveformAssetID, DurationSeconds: o.DurationSeconds}
	}
	event := &managev1.MediaProcessingLifecycleEvent{CorrelationId: v.CorrelationID, EntityType: managev1.TranscodeEntityType(v.EntityType), EntityId: v.EntityID, FileId: v.FileID, Status: commonv1.MediaProcessingStatus(v.Status), Outputs: outputs, TimestampMs: v.TimestampMS, SlotId: v.SlotID, AttemptId: v.AttemptID, TrackId: v.TrackID, ReleaseId: v.ReleaseID}
	return p.publisher.NotifyProtobuf(ctx, eventpkg.SignalMediaProcessingLifecycle, event)
}
