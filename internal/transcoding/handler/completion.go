package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/hls"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

type transcodeCommand interface {
	GetEventId() string
	GetEntityType() apiv1.TranscodeEntityType
	GetEntityId() string
	GetFileId() string
}

type completionService struct {
	storage   StorageClient
	publisher EventPublisher
}

func newTranscodeCompletion(
	command transcodeCommand,
	eventType apiv1.TranscodeEventType,
	outputs *apiv1.TranscodeOutputs,
	startedAt time.Time,
) *apiv1.TranscodeCompleteEvent {
	return &apiv1.TranscodeCompleteEvent{
		EventId:          command.GetEventId(),
		EventType:        eventType,
		EntityType:       command.GetEntityType(),
		EntityId:         command.GetEntityId(),
		FileId:           command.GetFileId(),
		Success:          true,
		Outputs:          outputs,
		ProcessingTimeMs: time.Since(startedAt).Milliseconds(),
	}
}

func (s *completionService) uploadHLS(
	ctx context.Context,
	target *commonv1.MediaGenerationWriteTarget,
	segmentDir string,
	complete *apiv1.TranscodeCompleteEvent,
	reportProgress progressReporter,
	mediaType string,
) error {
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING, 0)
	pkg, err := hls.Inspect(segmentDir)
	if err != nil {
		return fmt.Errorf("validate %s hls output: %w", mediaType, err)
	}
	complete.Outputs.Hls = pkg.Result(target)
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(complete)
	if err != nil {
		return fmt.Errorf("encode %s completion: %w", mediaType, err)
	}
	if _, err := hls.Upload(ctx, s.storage, target, pkg, payload, func(percent int) {
		reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING, percent)
	}); err != nil {
		return fmt.Errorf("%s hls upload failed: %w", mediaType, err)
	}
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING, 100)
	return nil
}

func (s *completionService) publish(
	ctx context.Context,
	complete *apiv1.TranscodeCompleteEvent,
	mediaType string,
) error {
	if err := s.publisher.PublishComplete(ctx, complete); err != nil {
		return jobresult.Retry(fmt.Errorf("publish %s completion: %w", mediaType, err))
	}
	return nil
}
