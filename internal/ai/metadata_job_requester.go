package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// StartMetadataGenerationCommand queues metadata suggestions for Target.
// Context carries the metadata-json task; Prompt is used when it is empty.
type StartMetadataGenerationCommand struct {
	Target  *managev1.AIResourceTarget
	Context string
	Prompt  string
}

// StartMetadataGeneration records a queued job and its durable generation
// command in one transaction.
func (s *Service) StartMetadataGeneration(ctx context.Context, command StartMetadataGenerationCommand) (_ *managev1.MetadataGenerationJob, retErr error) {
	// Failures can quote the requested context and prompt, which the boundary
	// does not treat as private fields.
	defer failurecause.AnnotatePrivate(&retErr, command.Context, command.Prompt)
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}
	job, err := mutation.Run(ctx, s.mutations, mutation.Spec[*metadataJobRecord]{
		Access: mutation.Checked(func(ctx context.Context, _ *gorm.DB) error {
			allowed, err := canUseAI(ctx, s.spiceDB, user, command.Target)
			if err != nil {
				return err
			}
			if !allowed {
				return domainerr.Denied("permission denied")
			}
			return nil
		}),
		Change: func(_ context.Context, tx *gorm.DB) (*metadataJobRecord, error) {
			job, err := newMetadataJob(user, command)
			if err != nil {
				return nil, err
			}
			if err := tx.Create(job).Error; err != nil {
				return nil, fmt.Errorf("failed to create metadata AI job: %w", err)
			}
			return job, nil
		},
		// Metadata jobs are operational queue rows, not audited content.
		Audit: func(*metadataJobRecord) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, tx *gorm.DB, job *metadataJobRecord) error {
			return publishDurableProtoInTransaction(
				ctx, s.publisher, tx, eventpkg.QueueAiMetadataGenerate, job.ID,
				&managev1.MetadataGenerationQueueEvent{JobId: job.ID},
			)
		},
	})
	if err != nil {
		if mutation.IsClassified(err) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to queue metadata AI job: %w", err)
	}

	slog.Info("Metadata AI job queued",
		"job_id", job.ID,
		"requester_member_id", user.MemberID.String(),
		"target_type", job.TargetType,
		"target_id", job.TargetID,
		"requested_keys", job.RequestedKeys,
	)
	return toProtoJob(job, true), nil
}

func newMetadataJob(user *auth.UserInfo, command StartMetadataGenerationCommand) (*metadataJobRecord, error) {
	// Only an administrator is allowed without a target; the job still
	// needs one to record.
	if command.Target == nil {
		return nil, domainerr.Required("target")
	}
	userPrompt := metadataAIUserPrompt(command.Context, command.Prompt)
	if err := validateMetadataAIUserPrompt(userPrompt); err != nil {
		return nil, err
	}
	payload, err := parseMetadataContextPayload(userPrompt)
	if err != nil {
		return nil, domainerr.InvalidField("context", "metadata-json requires a valid structured JSON payload")
	}
	return &metadataJobRecord{
		ID:                uuid.NewString(),
		RequesterMemberID: user.MemberID.String(),
		TargetType:        command.Target.Type.String(),
		TargetID:          strings.TrimSpace(command.Target.Id),
		RequestedKeys:     append([]string(nil), payload.Task.RequestedKeys...),
		Context:           command.Context,
		Prompt:            command.Prompt,
		Status:            metadataAIJobStatusQueued,
	}, nil
}

// GetMetadataGenerationJobQuery reads one of the requester's jobs.
type GetMetadataGenerationJobQuery struct {
	JobID string
}

// GetMetadataGenerationJob reads a job the principal requested.
func (s *Service) GetMetadataGenerationJob(ctx context.Context, query GetMetadataGenerationJobQuery) (*managev1.MetadataGenerationJob, error) {
	job, err := loadJobForRequester(ctx, s.db, auth.GetUser(ctx), query.JobID)
	if err != nil {
		return nil, err
	}
	return toProtoJob(job, true), nil
}

// ResolveMetadataGenerationJobCommand applies or dismisses a ready job.
type ResolveMetadataGenerationJobCommand struct {
	JobID      string
	Resolution managev1.MetadataGenerationJobResolution
}

// ResolveMetadataGenerationJob moves a ready job the principal requested to
// applied or dismissed.
func (s *Service) ResolveMetadataGenerationJob(ctx context.Context, command ResolveMetadataGenerationJobCommand) (*managev1.MetadataGenerationJob, error) {
	user := auth.GetUser(ctx)
	var job *metadataJobRecord
	resolved, err := mutation.Run(ctx, s.mutations, mutation.Spec[*metadataJobRecord]{
		// The job's requester is the only principal that may resolve it.
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			job, err = loadJobForRequester(ctx, tx, user, command.JobID)
			return err
		}),
		Change: func(_ context.Context, tx *gorm.DB) (*metadataJobRecord, error) {
			return resolveMetadataJob(tx, job, command.Resolution)
		},
		// Metadata jobs are operational queue rows, not audited content.
		Audit: func(*metadataJobRecord) mutation.Audit { return mutation.Unchanged() },
	})
	if err != nil {
		return nil, err
	}
	slog.Info("Metadata AI job resolved",
		"job_id", resolved.ID,
		"requester_member_id", user.MemberID.String(),
		"status", resolved.Status,
	)
	return toProtoJob(resolved, true), nil
}

func resolveMetadataJob(
	tx *gorm.DB,
	job *metadataJobRecord,
	resolution managev1.MetadataGenerationJobResolution,
) (*metadataJobRecord, error) {
	if job.Status != metadataAIJobStatusReady {
		return nil, domainerr.InvalidField("job_id", "metadata AI job is not ready to apply or dismiss")
	}
	now := dbtime.Now()
	nextStatus := metadataAIJobStatusDismissed
	if resolution == managev1.MetadataGenerationJobResolution_METADATA_GENERATION_JOB_RESOLUTION_APPLIED {
		nextStatus = metadataAIJobStatusApplied
	} else if resolution != managev1.MetadataGenerationJobResolution_METADATA_GENERATION_JOB_RESOLUTION_DISMISSED {
		return nil, domainerr.InvalidField("resolution", "unsupported metadata AI resolution")
	}
	result := tx.Model(&metadataJobRecord{}).
		Where("id = ? AND status = ?", job.ID, metadataAIJobStatusReady).
		Updates(structured.Fields{"status": nextStatus, "resolved_at": now, "updated_at": now})
	if result.Error != nil {
		return nil, fmt.Errorf("failed to resolve metadata AI job: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, domainerr.Precondition("metadata AI job is no longer ready to apply or dismiss")
	}
	job.Status = nextStatus
	job.ResolvedAt = &now
	job.UpdatedAt = now
	return job, nil
}

func loadJobForRequester(
	ctx context.Context,
	db *gorm.DB,
	user *auth.UserInfo,
	jobID string,
) (*metadataJobRecord, error) {
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, domainerr.Required("job_id")
	}

	var job metadataJobRecord
	if err := db.WithContext(ctx).First(&job, "id = ?", jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("metadata_ai_job", jobID)
		}
		return nil, fmt.Errorf("failed to load metadata AI job: %w", err)
	}
	if job.RequesterMemberID != user.MemberID.String() {
		return nil, domainerr.Denied("permission denied")
	}
	return &job, nil
}

func toProtoJob(
	job *metadataJobRecord,
	includeSuggestion bool,
) *managev1.MetadataGenerationJob {
	if job == nil {
		return nil
	}

	message := &managev1.MetadataGenerationJob{
		Id:                job.ID,
		Target:            &managev1.AIResourceTarget{Type: resolveAIResourceType(job.TargetType), Id: job.TargetID},
		RequesterMemberId: job.RequesterMemberID,
		Status:            resolveMetadataAIJobStatus(job.Status),
		RequestedKeys:     append([]string(nil), job.RequestedKeys...),
		CreatedAt:         timestamppb.New(job.CreatedAt),
		UpdatedAt:         timestamppb.New(job.UpdatedAt),
	}
	if includeSuggestion && len(job.Suggestion) > 0 {
		message.Suggestion = buildMetadataSuggestionMessage(job.Suggestion)
	}
	if job.Error != nil && strings.TrimSpace(*job.Error) != "" {
		message.Error = job.Error
	}
	if job.Provider != nil && strings.TrimSpace(*job.Provider) != "" {
		message.Provider = job.Provider
	}
	if job.Model != nil && strings.TrimSpace(*job.Model) != "" {
		message.Model = job.Model
	}
	if job.DurationMS != nil {
		message.DurationMs = job.DurationMS
	}
	if job.StartedAt != nil {
		message.StartedAt = timestamppb.New(*job.StartedAt)
	}
	if job.CompletedAt != nil {
		message.CompletedAt = timestamppb.New(*job.CompletedAt)
	}
	if job.ResolvedAt != nil {
		message.ResolvedAt = timestamppb.New(*job.ResolvedAt)
	}
	return message
}

func resolveMetadataAIJobStatus(status string) managev1.MetadataGenerationJobStatus {
	switch status {
	case metadataAIJobStatusQueued:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_QUEUED
	case metadataAIJobStatusRunning:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_RUNNING
	case metadataAIJobStatusReady:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_READY
	case metadataAIJobStatusFailed:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_FAILED
	case metadataAIJobStatusApplied:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_APPLIED
	case metadataAIJobStatusDismissed:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_DISMISSED
	default:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_UNSPECIFIED
	}
}

func resolveAIResourceType(value string) managev1.AIResourceType {
	switch value {
	case managev1.AIResourceType_AI_RESOURCE_TYPE_POST.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_POST
	case managev1.AIResourceType_AI_RESOURCE_TYPE_WORK.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_WORK
	case managev1.AIResourceType_AI_RESOURCE_TYPE_PAGE.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_PAGE
	case managev1.AIResourceType_AI_RESOURCE_TYPE_FORM.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_FORM
	case managev1.AIResourceType_AI_RESOURCE_TYPE_ARTIST.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_ARTIST
	case managev1.AIResourceType_AI_RESOURCE_TYPE_RELEASE.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_RELEASE
	case managev1.AIResourceType_AI_RESOURCE_TYPE_LABEL.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_LABEL
	case managev1.AIResourceType_AI_RESOURCE_TYPE_CAMPAIGN.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_CAMPAIGN
	case managev1.AIResourceType_AI_RESOURCE_TYPE_EMAIL_TEMPLATE.String():
		return managev1.AIResourceType_AI_RESOURCE_TYPE_EMAIL_TEMPLATE
	default:
		return managev1.AIResourceType_AI_RESOURCE_TYPE_UNSPECIFIED
	}
}
