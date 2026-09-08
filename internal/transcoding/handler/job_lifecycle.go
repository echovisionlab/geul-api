package handler

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobregistry"
)

type transcodeSession struct {
	*jobregistry.Session
	reportProgress progressReporter
}

type jobCoordinator struct {
	jobs        *jobregistry.Registry
	completions *completionService
	progress    progressPublisher
}

func (c *jobCoordinator) beginTranscodeJob(
	ctx context.Context,
	command transcodeCommand,
	completionKey string,
	expected transcodeCompletionExpectation,
) (*transcodeSession, error) {
	session, started := c.jobs.Start(ctx, command.GetEventId(), command.GetFileId())
	if !started {
		return nil, nil
	}

	replayed, err := c.completions.replay(session.Context, command, completionKey, expected)
	if err != nil || replayed {
		session.Close()
		return nil, err
	}
	return &transcodeSession{
		Session: session,
		reportProgress: c.progress.newReporter(
			session.Context,
			command.GetEventId(),
			command.GetEntityType(),
			command.GetEntityId(),
			command.GetFileId(),
		),
	}, nil
}
