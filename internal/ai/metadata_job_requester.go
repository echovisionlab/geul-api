package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func (m *MetadataJobManager) StartJob(
	ctx context.Context,
	user *auth.UserInfo,
	req *managev1.StartMetadataGenerationRequest,
) (*managev1.MetadataGenerationJob, error) {
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}
	if req == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}

	allowed, err := canUseAI(ctx, m.spiceDB, user, req.Target)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errs.PermissionDenied(errs.MsgPermissionDenied)
	}

	userPrompt := metadataAIUserPrompt(req.Context, req.Prompt)
	if err := validateMetadataAIUserPrompt(userPrompt); err != nil {
		return nil, err
	}

	payload, err := parseMetadataContextPayload(userPrompt)
	if err != nil {
		return nil, errs.InvalidArgument("context", "metadata-json requires a valid structured JSON payload")
	}

	job := &metadataJobRecord{
		ID:                uuid.NewString(),
		RequesterMemberID: user.MemberID.String(),
		TargetType:        req.Target.Type.String(),
		TargetID:          strings.TrimSpace(req.Target.Id),
		RequestedKeys:     append([]string(nil), payload.Task.RequestedKeys...),
		Context:           req.Context,
		Prompt:            req.Prompt,
		Status:            metadataAIJobStatusQueued,
	}
	if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(job).Error; err != nil {
			return fmt.Errorf("failed to create metadata AI job: %w", err)
		}
		return publishDurableProtoInTransaction(
			ctx,
			m.asyncPublisher,
			tx,
			eventpkg.QueueAiMetadataGenerate,
			job.ID,
			&managev1.MetadataGenerationQueueEvent{JobId: job.ID},
		)
	}); err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to queue metadata AI job: %w", err))
	}

	jobMessage := m.toProtoJob(job, true)

	slog.Info("Metadata AI job queued",
		"job_id", job.ID,
		"requester_member_id", user.MemberID.String(),
		"target_type", job.TargetType,
		"target_id", job.TargetID,
		"requested_keys", job.RequestedKeys,
	)

	return jobMessage, nil
}

func (m *MetadataJobManager) GetJobForRequester(
	ctx context.Context,
	user *auth.UserInfo,
	jobID string,
) (*managev1.MetadataGenerationJob, error) {
	job, err := m.loadJobForRequester(ctx, user, jobID)
	if err != nil {
		return nil, err
	}
	return m.toProtoJob(job, true), nil
}

func (m *MetadataJobManager) ResolveJobForRequester(
	ctx context.Context,
	user *auth.UserInfo,
	jobID string,
	resolution managev1.MetadataGenerationJobResolution,
) (*managev1.MetadataGenerationJob, error) {
	job, err := m.loadJobForRequester(ctx, user, jobID)
	if err != nil {
		return nil, err
	}
	if job.Status != metadataAIJobStatusReady {
		return nil, errs.InvalidArgument("job_id", "metadata AI job is not ready to apply or dismiss")
	}

	now := time.Now()
	nextStatus := metadataAIJobStatusDismissed
	if resolution == managev1.MetadataGenerationJobResolution_METADATA_GENERATION_JOB_RESOLUTION_APPLIED {
		nextStatus = metadataAIJobStatusApplied
	} else if resolution != managev1.MetadataGenerationJobResolution_METADATA_GENERATION_JOB_RESOLUTION_DISMISSED {
		return nil, errs.InvalidArgument("resolution", "unsupported metadata AI resolution")
	}

	updates := structured.Fields{
		"status":      nextStatus,
		"resolved_at": now,
		"updated_at":  now,
	}
	result := m.db.WithContext(ctx).
		Model(&metadataJobRecord{}).
		Where("id = ? AND status = ?", job.ID, metadataAIJobStatusReady).
		Updates(updates)
	if result.Error != nil {
		return nil, errs.Internal(fmt.Errorf("failed to resolve metadata AI job: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return nil, errs.FailedPrecondition("metadata AI job is no longer ready to apply or dismiss")
	}

	job.Status = nextStatus
	job.ResolvedAt = &now
	job.UpdatedAt = now

	slog.Info("Metadata AI job resolved",
		"job_id", job.ID,
		"requester_member_id", user.MemberID.String(),
		"status", nextStatus,
	)

	return m.toProtoJob(job, true), nil
}

func (m *MetadataJobManager) loadJobForRequester(
	ctx context.Context,
	user *auth.UserInfo,
	jobID string,
) (*metadataJobRecord, error) {
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errs.Required("job_id")
	}

	var job metadataJobRecord
	if err := m.db.WithContext(ctx).First(&job, "id = ?", jobID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("metadata_ai_job", jobID)
		}
		return nil, errs.Internal(fmt.Errorf("failed to load metadata AI job: %w", err))
	}
	if job.RequesterMemberID != user.MemberID.String() {
		return nil, errs.PermissionDenied(errs.MsgPermissionDenied)
	}
	return &job, nil
}

func (m *MetadataJobManager) toProtoJob(
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
