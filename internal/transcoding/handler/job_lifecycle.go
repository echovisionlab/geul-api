package handler

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobregistry"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
)

type transcodeSession struct {
	*jobregistry.Session
	reportProgress progressReporter
}

type jobCoordinator struct {
	jobs        *jobregistry.Registry
	completions *completionService
	progress    progressPublisher
	admission   JobAdmission
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

	decision, err := c.admission.Admit(ctx, transcodeJobIdentity(command))
	if err != nil {
		session.Close()
		return nil, jobresult.Retry(fmt.Errorf("check transcode job admission: %w", err))
	}
	switch decision {
	case JobAdmissionProceed:
	case JobAdmissionSettled:
		session.Close()
		return nil, nil
	case JobAdmissionIdentityMismatch:
		session.Close()
		return nil, ErrJobAdmissionIdentityMismatch
	default:
		session.Close()
		return nil, fmt.Errorf("invalid transcode job admission decision %d", decision)
	}

	// An explicit cancellation applies to unfinished work, while an already
	// committed terminal allocation takes precedence over a success receipt.
	// For an otherwise active allocation, keep receipt replay on the delivery
	// context so process shutdown can still redeliver it.
	replayed, err := c.completions.replay(ctx, command, completionKey, expected)
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

func transcodeJobIdentity(command transcodeCommand) JobIdentity {
	return JobIdentity{
		EventID:    command.GetEventId(),
		EntityType: command.GetEntityType(),
		EntityID:   command.GetEntityId(),
		FileID:     command.GetFileId(),
	}
}
