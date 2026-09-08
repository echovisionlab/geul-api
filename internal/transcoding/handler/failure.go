package handler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (p *audioProcessor) fail(
	ctx context.Context,
	job *apiv1.TranscodeAudioEvent,
	startedAt time.Time,
	cause error,
) error {
	return p.completions.publishFailure(
		ctx,
		job,
		apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		"audio",
		"Audio job failed",
		startedAt,
		cause,
	)
}

func (s *completionService) publishFailure(
	ctx context.Context,
	command transcodeCommand,
	eventType apiv1.TranscodeEventType,
	mediaType string,
	logMessage string,
	startedAt time.Time,
	cause error,
) error {
	duration := time.Since(startedAt).Milliseconds()
	slog.Error(logMessage,
		"media_type", mediaType,
		"job_id", command.GetEventId(),
		"entity_type", command.GetEntityType().String(),
		"entity_id", command.GetEntityId(),
		"file_id", command.GetFileId(),
		"duration_ms", duration,
		"error", cause,
	)
	errorMessage := cause.Error()
	publishErr := s.publisher.PublishComplete(ctx, &apiv1.TranscodeCompleteEvent{
		EventId:          command.GetEventId(),
		EventType:        eventType,
		EntityType:       command.GetEntityType(),
		EntityId:         command.GetEntityId(),
		FileId:           command.GetFileId(),
		Success:          false,
		Error:            &errorMessage,
		ProcessingTimeMs: duration,
	})
	if publishErr != nil {
		slog.Error("Failed to publish failure event", "error", publishErr)
		return jobresult.Retry(fmt.Errorf("publish %s failure result: %w; job failure: %v", mediaType, publishErr, cause))
	}
	return jobresult.Terminal(cause)
}

func (p *videoProcessor) fail(
	ctx context.Context,
	job *apiv1.TranscodeVideoEvent,
	startedAt time.Time,
	cause error,
) error {
	return p.completions.publishFailure(
		ctx,
		job,
		apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		"video",
		"Video job failed",
		startedAt,
		cause,
	)
}
