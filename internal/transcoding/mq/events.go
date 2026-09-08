package mq

import (
	"context"
	"fmt"
	"log/slog"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// PublishComplete enqueues a transcode completion result.
func (p *Publisher) PublishComplete(ctx context.Context, value *apiv1.TranscodeCompleteEvent) error {
	ensureTimestamp(&value.TimestampMs)
	if err := p.enqueue(ctx, eventpkg.QueueTranscodeResult, value.EventId, "api.manage.v1.TranscodeCompleteEvent", value); err != nil {
		return fmt.Errorf("publish transcode result: %w", err)
	}
	slog.Info("Enqueued transcode result", "event_id", value.EventId, "success", value.Success)
	return nil
}

// PublishProgress sends a transient transcode progress notification.
func (p *Publisher) PublishProgress(ctx context.Context, value *apiv1.TranscodeProgressEvent) error {
	ensureTimestamp(&value.TimestampMs)
	return p.notify(ctx, eventpkg.SignalTranscodeProgress, value.EventId, "api.manage.v1.TranscodeProgressEvent", value)
}

// PublishWaveformProgress sends a transient waveform progress notification.
func (p *Publisher) PublishWaveformProgress(ctx context.Context, value *apiv1.WaveformProgressEvent) error {
	ensureTimestamp(&value.TimestampMs)
	return p.notify(ctx, eventpkg.SignalWaveformProgress, value.EventId, "api.manage.v1.WaveformProgressEvent", value)
}

// PublishWaveformComplete enqueues a successful waveform result.
func (p *Publisher) PublishWaveformComplete(ctx context.Context, value *apiv1.WaveformCompleteEvent) error {
	ensureTimestamp(&value.TimestampMs)
	result := &apiv1.WaveformResultEvent{Outcome: &apiv1.WaveformResultEvent_Completed{Completed: value}}
	return p.enqueue(ctx, eventpkg.QueueWaveformResult, value.EventId, "api.manage.v1.WaveformResultEvent", result)
}

// PublishWaveformFail enqueues a failed waveform result.
func (p *Publisher) PublishWaveformFail(ctx context.Context, value *apiv1.WaveformFailEvent) error {
	ensureTimestamp(&value.TimestampMs)
	result := &apiv1.WaveformResultEvent{Outcome: &apiv1.WaveformResultEvent_Failed{Failed: value}}
	return p.enqueue(ctx, eventpkg.QueueWaveformResult, value.EventId, "api.manage.v1.WaveformResultEvent", result)
}
