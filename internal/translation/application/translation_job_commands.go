package application

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// CancelTranslationJobCommand cancels a queued or running translation job.
type CancelTranslationJobCommand struct {
	JobID string
}

// CancelTranslationJob cancels an in-flight job under the entity's
// regeneration authority.
func (s *Service) CancelTranslationJob(ctx context.Context, command CancelTranslationJobCommand) error {
	outcome := "failed"
	defer func() { s.metrics.recordAdminAction(ctx, "cancel", outcome) }()
	if _, err := requireAuthenticatedTranslationRequester(ctx); err != nil {
		return err
	}
	jobID := strings.TrimSpace(command.JobID)
	if jobID == "" {
		return domainerr.InvalidField("job_id", "job_id is required")
	}
	now := s.now().UTC()
	var candidate Job
	job, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Job, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			if candidate, err = loadTranslationJobForCommand(ctx, store, jobID, false); err != nil {
				return err
			}
			return store.Domains().LockRoot(ctx, candidate.EntityType, candidate.EntityID)
		},
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return requireTranslationRegenerationAuthority(ctx, store, candidate.EntityType, candidate.EntityID)
		}),
		Change: func(ctx context.Context, store Store) (Job, error) {
			job, err := loadTranslationJobForCommand(ctx, store, jobID, true)
			if err != nil {
				return Job{}, err
			}
			deleted, err := store.DeleteJob(ctx, job.ID, JobQueued, JobRunning)
			if err != nil {
				return Job{}, err
			}
			if deleted != 1 {
				return Job{}, domainerr.Precondition("translation job is already terminal")
			}
			job.Status = JobCancelled
			job.CompletedAt = &now
			job.UpdatedAt = now
			return job, nil
		},
		// Translation jobs are operational queue rows, not audited content.
		Audit: func(Job) mutation.Audit { return mutation.Unchanged() },
	})
	if err != nil {
		return err
	}
	emitTranslationJobTerminal(
		ctx, &job, translationJobTerminalOutcomeCancelled, "", translationJobStartedAt(&job, now), now,
	)
	outcome = "succeeded"
	return nil
}

func requireAuthenticatedTranslationRequester(ctx context.Context) (string, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return "", domainerr.AuthenticationRequired()
	}
	if user.Banned {
		return "", domainerr.AccountBanned()
	}
	if !user.Onboarded {
		return "", domainerr.NoPermission("edit", "translation")
	}
	return user.MemberID.String(), nil
}

func loadTranslationJobForCommand(
	ctx context.Context,
	store Store,
	jobID string,
	locked bool,
) (Job, error) {
	if jobID == "" {
		return Job{}, domainerr.InvalidField("job_id", "job_id is required")
	}
	job, err := store.JobForCommand(ctx, jobID, locked)
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return Job{}, domainerr.ResourceNotFound("translation_job", jobID)
		}
		return Job{}, err
	}
	if err := ValidateJobRequester(&job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func requireAvailableTranslationProvider(ctx context.Context, store JobStore) error {
	available, err := hasAvailableTranslationProvider(ctx, store)
	if err != nil {
		return err
	}
	if !available {
		return domainerr.Precondition(translationProviderUnavailableMessage)
	}
	return nil
}
