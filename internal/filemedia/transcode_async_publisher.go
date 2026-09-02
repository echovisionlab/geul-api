package filemedia

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type transcodeQueuePublisherAdapter struct {
	publisher mq.TranscoderPublisher
}

// fileIngestTranscodeJobRegistrar records the completion authority beside the
// File-owned output allocations. It deliberately does not publish: the caller
// enqueues the identical command through the same outer transaction.
type fileIngestTranscodeJobRegistrar interface {
	RegisterTranscodeAudio(context.Context, *gorm.DB, *managev1.TranscodeAudioEvent) error
	RegisterTranscodeVideo(context.Context, *gorm.DB, *managev1.TranscodeVideoEvent) error
}

func (s *FileService) fileIngestTranscodeJobRegistrar() (fileIngestTranscodeJobRegistrar, error) {
	registrar, ok := s.publisher.(fileIngestTranscodeJobRegistrar)
	if !ok {
		return nil, fmt.Errorf("transcode publisher does not register File ingest jobs")
	}
	return registrar, nil
}

func (adapter transcodeQueuePublisherAdapter) EnqueueProtobuf(
	ctx context.Context,
	queue string,
	_ string,
	message proto.Message,
) error {
	if adapter.publisher == nil {
		return fmt.Errorf("transcode publisher is required")
	}
	switch queue {
	case eventpkg.QueueTranscoderAudio:
		job, ok := message.(*managev1.TranscodeAudioEvent)
		if !ok {
			return fmt.Errorf("transcode audio command has type %T", message)
		}
		return adapter.publisher.PublishTranscodeAudio(ctx, job)
	case eventpkg.QueueTranscoderVideo:
		job, ok := message.(*managev1.TranscodeVideoEvent)
		if !ok {
			return fmt.Errorf("transcode video command has type %T", message)
		}
		return adapter.publisher.PublishTranscodeVideo(ctx, job)
	default:
		return fmt.Errorf("unsupported transcode queue %q", queue)
	}
}

func (transcodeQueuePublisherAdapter) NotifyProtobuf(context.Context, string, proto.Message) error {
	return fmt.Errorf("transcode publisher does not support realtime signals")
}

func (s *FileService) transcodeCommandPublisher() AsyncPublisher {
	if _, ok := s.asyncPublisher.(TransactionalAsyncPublisher); ok {
		return s.asyncPublisher
	}
	if s.publisher != nil {
		return transcodeQueuePublisherAdapter{publisher: s.publisher}
	}
	return s.asyncPublisher
}

func enqueueStableFileIngestAudioTranscodeJob(
	ctx context.Context,
	db *gorm.DB,
	publisher AsyncPublisher,
	registrar fileIngestTranscodeJobRegistrar,
	file model.File,
	entityType managev1.TranscodeEntityType,
	entityID string,
) (bool, error) {
	enqueued := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, shouldEnqueue, err := newStableFileIngestAudioTranscodeJob(
			ctx, tx, file, entityType, entityID,
		)
		if err != nil || !shouldEnqueue {
			return err
		}
		if err := registrar.RegisterTranscodeAudio(ctx, tx, job); err != nil {
			return err
		}
		if err := publishDurableProtoInTransaction(
			ctx, publisher, tx, eventpkg.QueueTranscoderAudio, job.GetEventId(), job,
		); err != nil {
			return err
		}
		enqueued = true
		return nil
	})
	return enqueued, err
}

func enqueueStableFileIngestVideoTranscodeJob(
	ctx context.Context,
	db *gorm.DB,
	publisher AsyncPublisher,
	registrar fileIngestTranscodeJobRegistrar,
	file model.File,
	entityType managev1.TranscodeEntityType,
	entityID string,
) (bool, error) {
	enqueued := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, shouldEnqueue, err := newStableFileIngestVideoTranscodeJob(
			ctx, tx, file, entityType, entityID,
		)
		if err != nil || !shouldEnqueue {
			return err
		}
		if err := registrar.RegisterTranscodeVideo(ctx, tx, job); err != nil {
			return err
		}
		if err := publishDurableProtoInTransaction(
			ctx, publisher, tx, eventpkg.QueueTranscoderVideo, job.GetEventId(), job,
		); err != nil {
			return err
		}
		enqueued = true
		return nil
	})
	return enqueued, err
}
