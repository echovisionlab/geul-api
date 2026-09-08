package waveform

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

func (p *Processor) replayCompletion(
	ctx context.Context,
	command *apiv1.WaveformGenerateEvent,
	key string,
) (bool, error) {
	payload, found, err := p.storage.Completion(ctx, key)
	if err != nil {
		return false, jobresult.Retry(fmt.Errorf("inspect waveform completion: %w", err))
	}
	if !found {
		return false, nil
	}
	var complete apiv1.WaveformCompleteEvent
	if err := proto.Unmarshal(payload, &complete); err != nil {
		return false, fmt.Errorf("decode waveform completion: %w", err)
	}
	if complete.GetEventId() != command.GetEventId() ||
		complete.GetEntityType() != command.GetEntityType() ||
		complete.GetEntityId() != command.GetEntityId() ||
		complete.GetFileId() != command.GetFileId() ||
		complete.GetOutput().GetAssetId() != command.GetOutput().GetAssetId() {
		return false, fmt.Errorf("waveform completion does not match command %s", command.GetEventId())
	}
	if err := p.publisher.PublishWaveformComplete(ctx, &complete); err != nil {
		return false, jobresult.Retry(fmt.Errorf("replay waveform complete: %w", err))
	}
	return true, nil
}

func (p *Processor) publishProgress(
	ctx context.Context,
	event *apiv1.WaveformGenerateEvent,
	step progressStep,
) {
	progressEvent := &apiv1.WaveformProgressEvent{
		EventId:        event.EventId,
		EntityType:     event.EntityType,
		EntityId:       event.EntityId,
		FileId:         event.FileId,
		SequenceNumber: step.sequence,
		Progress:       step.percent,
		Stage:          &step.stage,
		TimestampMs:    time.Now().UnixMilli(),
	}

	if err := p.publisher.PublishWaveformProgress(ctx, progressEvent); err != nil && !p.isCancelled(ctx, err) {
		slog.Warn("Failed to publish waveform progress event",
			"event_id", event.EventId,
			"entity_type", event.EntityType.String(),
			"entity_id", event.EntityId,
			"file_id", event.FileId,
			"progress", step.percent,
			"stage", step.stage.String(),
			"error", err,
		)
	}
}

func (p *Processor) publishFail(
	ctx context.Context,
	event *apiv1.WaveformGenerateEvent,
	startedAt time.Time,
	cause error,
) error {
	slog.ErrorContext(ctx, "Waveform generation failed",
		"job_id", event.EventId,
		"file_id", event.FileId,
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"error", cause,
	)
	failEvent := &apiv1.WaveformFailEvent{
		EventId:          event.EventId,
		EntityType:       event.EntityType,
		EntityId:         event.EntityId,
		FileId:           event.FileId,
		Error:            cause.Error(),
		ProcessingTimeMs: time.Since(startedAt).Milliseconds(),
		TimestampMs:      time.Now().UnixMilli(),
	}
	if publishErr := p.publisher.PublishWaveformFail(ctx, failEvent); publishErr != nil {
		return jobresult.Retry(fmt.Errorf("publish waveform failure result: %w; job failure: %v", publishErr, cause))
	}
	return jobresult.Terminal(cause)
}

func waveformWriteResult(target *commonv1.AssetWriteTarget, payload []byte) *commonv1.AssetWriteResult {
	sum := sha256.Sum256(payload)
	return &commonv1.AssetWriteResult{
		AssetId:  target.GetAssetId(),
		FileSize: int64(len(payload)),
		Sha256:   sum[:],
	}
}

func (p *Processor) isCancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled
}
