package handler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobregistry"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const transcodeCancellationResultTimeout = 5 * time.Second

func publishExplicitCancellationResult[T transcodeCommand](
	w transcodeWorkflow[T],
	ctx context.Context,
	command T,
	startedAt time.Time,
) error {
	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), transcodeCancellationResultTimeout)
	defer cancel()
	return w.fail(resultCtx, command, startedAt, jobregistry.ErrExplicitCancellation)
}

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
		startedAt,
		cause,
	)
}

func (s *completionService) publishFailure(
	ctx context.Context,
	command transcodeCommand,
	eventType apiv1.TranscodeEventType,
	mediaType string,
	startedAt time.Time,
	cause error,
) error {
	duration := time.Since(startedAt).Milliseconds()
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
	// The queue consumer logs the returned failure; it names the job here.
	attrs := []slog.Attr{
		slog.String("media_type", mediaType),
		slog.String("job_id", command.GetEventId()),
		slog.String("entity_type", command.GetEntityType().String()),
		slog.String("entity_id", command.GetEntityId()),
		slog.String("file_id", command.GetFileId()),
		slog.Int64("duration_ms", duration),
	}
	if publishErr != nil {
		return failurecause.WithAttrs(jobresult.Retry(fmt.Errorf("publish %s failure result: %w; job failure: %v", mediaType, publishErr, cause)), attrs...)
	}
	return failurecause.WithAttrs(jobresult.Terminal(cause), attrs...)
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
		startedAt,
		cause,
	)
}
