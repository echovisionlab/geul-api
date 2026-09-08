package handler

import (
	"context"
	"time"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type transcodeWorkflow[T transcodeCommand] struct {
	validate    func(T) error
	start       func(context.Context, T) (*transcodeSession, error)
	transcode   func(context.Context, T, progressReporter, time.Time) (*apiv1.TranscodeCompleteEvent, error)
	fail        func(context.Context, T, time.Time, error) error
	completions *completionService
	mediaType   string
}

func (w transcodeWorkflow[T]) run(ctx context.Context, command T) error {
	startedAt := time.Now()
	if err := w.validate(command); err != nil {
		return err
	}
	session, err := w.start(ctx, command)
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	defer session.Close()

	complete, err := w.transcode(session.Context, command, session.reportProgress, startedAt)
	if err != nil {
		return w.fail(session.Context, command, startedAt, err)
	}
	return w.completions.publish(session.Context, complete, w.mediaType)
}
