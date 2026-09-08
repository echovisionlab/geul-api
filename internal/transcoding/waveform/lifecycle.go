package waveform

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobregistry"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (p *Processor) beginGenerateJob(
	ctx context.Context,
	event *apiv1.WaveformGenerateEvent,
	completionKey string,
) (*jobregistry.Session, error) {
	session, started := p.jobs.Start(ctx, event.GetEventId(), event.GetEventId())
	if !started {
		return nil, nil
	}
	replayed, err := p.replayCompletion(session.Context, event, completionKey)
	if err != nil || replayed {
		session.Close()
		return nil, err
	}
	return session, nil
}

// CancelJob cancels an active waveform job by event ID.
func (p *Processor) CancelJob(eventID string) bool {
	return p.jobs.CancelEvent(eventID)
}
