package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// StartMetadataGenerationCommand queues metadata suggestions for Target.
// Context carries the metadata-json task; Prompt is used when it is empty.
type StartMetadataGenerationCommand struct {
	Target  *Target
	Context string
	Prompt  string
}

// StartMetadataGeneration records a queued job and its durable generation
// command in one transaction.
func (s *Service) StartMetadataGeneration(ctx context.Context, command StartMetadataGenerationCommand) (_ Job, retErr error) {
	// Failures can quote the requested context and prompt, which the boundary
	// does not treat as private fields.
	defer failurecause.AnnotatePrivate(&retErr, command.Context, command.Prompt)
	user := auth.GetUser(ctx)
	if user == nil {
		return Job{}, domainerr.AuthenticationRequired()
	}
	job, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Job, Store]{
		Access: mutation.Checked(func(ctx context.Context, _ Store) error {
			allowed, err := canUseAI(ctx, s.spiceDB, user, command.Target)
			if err != nil {
				return err
			}
			if !allowed {
				return domainerr.Denied("permission denied")
			}
			return nil
		}),
		Change: func(ctx context.Context, store Store) (Job, error) {
			job, err := newMetadataJob(user, command)
			if err != nil {
				return Job{}, err
			}
			stored, err := store.InsertJob(ctx, job)
			if err != nil {
				return Job{}, fmt.Errorf("failed to create metadata AI job: %w", err)
			}
			return stored, nil
		},
		// Metadata jobs are operational queue rows, not audited content.
		Audit: func(Job) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, store Store, job Job) error {
			return store.EnqueueGenerate(ctx, job.ID)
		},
	})
	if err != nil {
		if mutation.IsClassified(err) {
			return Job{}, err
		}
		return Job{}, fmt.Errorf("failed to queue metadata AI job: %w", err)
	}

	slog.Info("Metadata AI job queued",
		"job_id", job.ID,
		"requester_member_id", user.MemberID.String(),
		"target_type", job.TargetType,
		"target_id", job.TargetID,
		"requested_keys", job.RequestedKeys,
	)
	return job, nil
}

func newMetadataJob(user *auth.UserInfo, command StartMetadataGenerationCommand) (Job, error) {
	// Only an administrator is allowed without a target; the job still
	// needs one to record.
	if command.Target == nil {
		return Job{}, domainerr.Required("target")
	}
	userPrompt := metadataAIUserPrompt(command.Context, command.Prompt)
	if err := validateMetadataAIUserPrompt(userPrompt); err != nil {
		return Job{}, err
	}
	payload, err := parseMetadataContextPayload(userPrompt)
	if err != nil {
		return Job{}, domainerr.InvalidField("context", "metadata-json requires a valid structured JSON payload")
	}
	return Job{
		ID:                uuid.NewString(),
		RequesterMemberID: user.MemberID.String(),
		TargetType:        command.Target.Type,
		TargetID:          strings.TrimSpace(command.Target.ID),
		RequestedKeys:     append([]string(nil), payload.Task.RequestedKeys...),
		Context:           command.Context,
		Prompt:            command.Prompt,
		Status:            JobQueued,
	}, nil
}

// GetMetadataGenerationJobQuery reads one of the requester's jobs.
type GetMetadataGenerationJobQuery struct {
	JobID string
}

// GetMetadataGenerationJob reads a job the principal requested.
func (s *Service) GetMetadataGenerationJob(ctx context.Context, query GetMetadataGenerationJobQuery) (Job, error) {
	var job Job
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		job, err = loadJobForRequester(ctx, store, auth.GetUser(ctx), query.JobID)
		return err
	})
	return job, err
}

// ResolveMetadataGenerationJobCommand applies or dismisses a ready job.
type ResolveMetadataGenerationJobCommand struct {
	JobID      string
	Resolution Resolution
}

// ResolveMetadataGenerationJob moves a ready job the principal requested to
// applied or dismissed.
func (s *Service) ResolveMetadataGenerationJob(ctx context.Context, command ResolveMetadataGenerationJobCommand) (Job, error) {
	user := auth.GetUser(ctx)
	var job Job
	resolved, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Job, Store]{
		// The job's requester is the only principal that may resolve it.
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			job, err = loadJobForRequester(ctx, store, user, command.JobID)
			return err
		}),
		Change: func(ctx context.Context, store Store) (Job, error) {
			return resolveMetadataJob(ctx, store, job, command.Resolution)
		},
		// Metadata jobs are operational queue rows, not audited content.
		Audit: func(Job) mutation.Audit { return mutation.Unchanged() },
	})
	if err != nil {
		return Job{}, err
	}
	slog.Info("Metadata AI job resolved",
		"job_id", resolved.ID,
		"requester_member_id", user.MemberID.String(),
		"status", string(resolved.Status),
	)
	return resolved, nil
}

func resolveMetadataJob(
	ctx context.Context,
	store Store,
	job Job,
	resolution Resolution,
) (Job, error) {
	if job.Status != JobReady {
		return Job{}, domainerr.InvalidField("job_id", "metadata AI job is not ready to apply or dismiss")
	}
	now := dbtime.Now()
	nextStatus := JobDismissed
	if resolution == ResolutionApplied {
		nextStatus = JobApplied
	} else if resolution != ResolutionDismissed {
		return Job{}, domainerr.InvalidField("resolution", "unsupported metadata AI resolution")
	}
	resolved, err := store.ResolveJob(ctx, job.ID, nextStatus, now)
	if err != nil {
		return Job{}, fmt.Errorf("failed to resolve metadata AI job: %w", err)
	}
	if !resolved {
		return Job{}, domainerr.Precondition("metadata AI job is no longer ready to apply or dismiss")
	}
	job.Status = nextStatus
	job.ResolvedAt = &now
	job.UpdatedAt = now
	return job, nil
}

func loadJobForRequester(
	ctx context.Context,
	store Store,
	user *auth.UserInfo,
	jobID string,
) (Job, error) {
	if user == nil {
		return Job{}, domainerr.AuthenticationRequired()
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return Job{}, domainerr.Required("job_id")
	}
	job, err := store.Job(ctx, jobID)
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return Job{}, domainerr.ResourceNotFound("metadata_ai_job", jobID)
		}
		return Job{}, fmt.Errorf("failed to load metadata AI job: %w", err)
	}
	if job.RequesterMemberID != user.MemberID.String() {
		return Job{}, domainerr.Denied("permission denied")
	}
	return job, nil
}
