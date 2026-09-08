package waveform

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// HandleGenerateJob processes one waveform generation command.
func (p *Processor) HandleGenerateJob(ctx context.Context, event *apiv1.WaveformGenerateEvent) error {
	if err := validateWaveformEvent(event); err != nil {
		return err
	}
	startedAt := time.Now()
	session, err := p.beginGenerateJob(ctx, event, event.GetOutput().GetObjectKey())
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	defer session.Close()

	complete, _, err := p.generate(session.Context, event, startedAt)
	if err != nil {
		return p.failUnlessCancelled(session.Context, ctx, event, startedAt, err)
	}
	if session.Context.Err() != nil {
		return nil
	}
	if err := p.publisher.PublishWaveformComplete(ctx, complete); err != nil {
		return jobresult.Retry(fmt.Errorf("publish waveform complete: %w", err))
	}
	return nil
}

func (p *Processor) generate(
	ctx context.Context,
	event *apiv1.WaveformGenerateEvent,
	startedAt time.Time,
) (*apiv1.WaveformCompleteEvent, [][]float64, error) {
	workDir, err := p.createWorkDir(event, startedAt)
	if err != nil {
		return nil, nil, err
	}
	defer p.cleanupWorkDir(event.GetEventId())
	sourcePath, err := p.downloadSource(ctx, event, workDir, startedAt)
	if err != nil {
		return nil, nil, err
	}
	peaks, err := p.generatePeaks(ctx, event, sourcePath, startedAt)
	if err != nil {
		return nil, nil, err
	}
	payload, waveformPath, err := writeWaveformPayload(workDir, event.GetEventId(), peaks)
	if err != nil {
		return nil, nil, err
	}
	complete, err := p.uploadWaveform(ctx, event, waveformPath, payload, startedAt)
	return complete, peaks, err
}
