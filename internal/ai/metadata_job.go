package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

const (
	metadataAIJobStatusQueued    = "queued"
	metadataAIJobStatusRunning   = "running"
	metadataAIJobStatusReady     = "ready"
	metadataAIJobStatusFailed    = "failed"
	metadataAIJobStatusApplied   = "applied"
	metadataAIJobStatusDismissed = "dismissed"

	metadataAIJobLeaseDuration   = 3 * time.Minute
	metadataAIJobRecoveryDelay   = 10 * time.Minute
	metadataAIPersistenceTimeout = 5 * time.Second
	metadataAIJobRecoveryBatch   = 25
)

var errMetadataAIJobLeaseUnavailable = errors.New("metadata AI job is being processed by another delivery")

var metadataSuggestionResponseKeys = map[string]struct{}{
	"summary": {},
}

type MetadataJobManager struct {
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	provider       llm.Provider
	asyncPublisher AsyncPublisher
}

func NewMetadataJobManager(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	googleAIAPIKey string,
	asyncPublisher AsyncPublisher,
) (*MetadataJobManager, error) {
	provider, err := newAITextProvider(googleAIAPIKey)
	if err != nil {
		return nil, err
	}
	return NewMetadataJobManagerWithProvider(db, spiceDB, provider, asyncPublisher), nil
}

func newAITextProvider(apiKey string) (llm.Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("googleAIAPIKey must be configured for gemini provider")
	}
	return llm.NewGeminiProvider(llm.GeminiConfig{APIKey: apiKey})
}

func NewMetadataJobManagerWithProvider(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	provider llm.Provider,
	asyncPublisher AsyncPublisher,
) *MetadataJobManager {
	dependencycheck.New("ai.MetadataJobManager").
		RequireNotNil(db, "db").
		RequireNotNil(spiceDB, "spiceDB").
		RequireNotNil(provider, "provider").
		RequireNotNil(asyncPublisher, "asyncPublisher").
		Validate()
	return &MetadataJobManager{
		db:             db,
		spiceDB:        spiceDB,
		provider:       provider,
		asyncPublisher: asyncPublisher,
	}
}

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

func (m *MetadataJobManager) ProcessJob(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}

	var job metadataJobRecord
	if err := m.db.WithContext(ctx).First(&job, "id = ?", jobID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return fmt.Errorf("failed to load metadata AI job %s: %w", jobID, err)
	}
	if job.Status != metadataAIJobStatusQueued && job.Status != metadataAIJobStatusRunning {
		return nil
	}

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	staleBefore := startedAt.Add(-metadataAIJobLeaseDuration)
	if job.Status == metadataAIJobStatusRunning && job.StartedAt != nil && job.StartedAt.After(staleBefore) {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}
	claimed, err := m.claimJob(ctx, &job, startedAt)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	userPrompt := metadataAIUserPrompt(job.Context, job.Prompt)

	responseSchema := buildMetadataResponseJSONSchema(userPrompt)
	responseText, err := m.provider.GenerateText(ctx, llm.GenerationRequest{
		RequestID:          job.ID,
		Action:             "metadata-json",
		SystemPrompt:       metadataAISystemPrompt,
		UserPrompt:         userPrompt,
		ResponseJSONSchema: responseSchema,
		Timeout:            metadataAIProviderTimeout,
		Observer:           metadataAIProviderObserver{},
	})
	if err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}
	if err := ctx.Err(); err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}

	suggestion, err := parseMetadataSuggestionPayload(responseText, job.RequestedKeys)
	if err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}
	if err := ctx.Err(); err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}

	completedAt := time.Now()
	durationMS := time.Since(startedAt).Milliseconds()
	suggestionJSON, err := json.Marshal(suggestion)
	if err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}
	updates := structured.Fields{
		"status":        metadataAIJobStatusReady,
		"suggestion":    string(suggestionJSON),
		"response_text": responseText,
		"error":         nil,
		"duration_ms":   durationMS,
		"completed_at":  completedAt,
		"updated_at":    completedAt,
	}
	result := m.db.WithContext(ctx).
		Model(&metadataJobRecord{}).
		Where("id = ? AND status = ? AND started_at = ?", job.ID, metadataAIJobStatusRunning, startedAt).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to finalize metadata AI job: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	job.Status = metadataAIJobStatusReady
	job.Suggestion = suggestion
	job.ResponseText = &responseText
	job.Error = nil
	job.DurationMS = &durationMS
	job.CompletedAt = &completedAt
	job.UpdatedAt = completedAt

	slog.Info("Metadata AI job completed",
		"job_id", job.ID,
		"target_type", job.TargetType,
		"target_id", job.TargetID,
		"duration_ms", durationMS,
		"requested_keys", job.RequestedKeys,
	)

	return nil
}

// RecoverExpiredJobs republishes durable commands only after their queue retry
// window has elapsed and no transport row with the job's stable message ID
// remains in PGMQ. The job timestamp update and enqueue share one transaction.
func (m *MetadataJobManager) RecoverExpiredJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = metadataAIJobRecoveryBatch
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	recoveryBefore := now.Add(-metadataAIJobRecoveryDelay)
	staleBefore := now.Add(-metadataAIJobLeaseDuration)
	queueTable := pq.QuoteIdentifier("q_" + eventpkg.QueueAiMetadataGenerate)
	republished := 0
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []metadataJobRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(
				"(status = ? AND updated_at <= ?) OR (status = ? AND updated_at <= ? AND (started_at IS NULL OR started_at <= ?))",
				metadataAIJobStatusQueued,
				recoveryBefore,
				metadataAIJobStatusRunning,
				recoveryBefore,
				staleBefore,
			).
			Order("updated_at ASC").
			Limit(limit).
			Find(&jobs).Error; err != nil {
			return fmt.Errorf("failed to scan expired metadata AI jobs: %w", err)
		}

		for _, job := range jobs {
			var transportPending bool
			query := "SELECT EXISTS (SELECT 1 FROM pgmq." + queueTable + " WHERE message->>'message_id' = ?)"
			if err := tx.Raw(query, job.ID).Scan(&transportPending).Error; err != nil {
				return fmt.Errorf("failed to inspect metadata AI queue message %s: %w", job.ID, err)
			}

			update := tx.Model(&metadataJobRecord{}).
				Where("id = ? AND status = ? AND updated_at = ?", job.ID, job.Status, job.UpdatedAt)
			if job.Status == metadataAIJobStatusRunning {
				if job.StartedAt == nil {
					update = update.Where("started_at IS NULL")
				} else {
					update = update.Where("started_at = ?", *job.StartedAt)
				}
			}
			result := update.Updates(structured.Fields{"updated_at": now})
			if result.Error != nil {
				return fmt.Errorf("failed to refresh expired metadata AI job %s: %w", job.ID, result.Error)
			}
			if result.RowsAffected == 0 || transportPending {
				continue
			}

			if err := publishDurableProtoInTransaction(
				ctx,
				m.asyncPublisher,
				tx,
				eventpkg.QueueAiMetadataGenerate,
				job.ID,
				&managev1.MetadataGenerationQueueEvent{JobId: job.ID},
			); err != nil {
				return fmt.Errorf("failed to republish expired metadata AI job %s: %w", job.ID, err)
			}
			republished++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if republished > 0 {
		slog.Info("Expired metadata AI jobs republished", "count", republished)
	}
	return republished, nil
}

func (m *MetadataJobManager) claimJob(
	ctx context.Context,
	job *metadataJobRecord,
	startedAt time.Time,
) (bool, error) {
	query := m.db.WithContext(ctx).Model(&metadataJobRecord{})
	switch job.Status {
	case metadataAIJobStatusQueued:
		query = query.Where("id = ? AND status = ?", job.ID, metadataAIJobStatusQueued)
	case metadataAIJobStatusRunning:
		query = query.Where("id = ? AND status = ?", job.ID, metadataAIJobStatusRunning)
		if job.StartedAt == nil {
			query = query.Where("started_at IS NULL")
		} else {
			query = query.Where("started_at = ?", *job.StartedAt)
		}
	default:
		return false, nil
	}
	result := query.Updates(structured.Fields{
		"status":     metadataAIJobStatusRunning,
		"provider":   m.provider.ProviderName(),
		"model":      m.provider.ModelName(),
		"started_at": startedAt,
		"updated_at": startedAt,
	})
	if result.Error != nil {
		return false, fmt.Errorf("failed to mark metadata AI job running: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return false, nil
	}

	job.Status = metadataAIJobStatusRunning
	job.Provider = new(m.provider.ProviderName())
	job.Model = new(m.provider.ModelName())
	job.StartedAt = &startedAt
	job.UpdatedAt = startedAt
	return true, nil
}

func (m *MetadataJobManager) failJob(
	ctx context.Context,
	job *metadataJobRecord,
	startedAt time.Time,
	duration time.Duration,
	cause error,
) error {
	finishedAt := time.Now()
	durationMS := duration.Milliseconds()
	message := cause.Error()
	updates := structured.Fields{
		"status":       metadataAIJobStatusFailed,
		"error":        message,
		"duration_ms":  durationMS,
		"completed_at": finishedAt,
		"updated_at":   finishedAt,
	}
	persistenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metadataAIPersistenceTimeout)
	defer cancel()
	result := m.db.WithContext(persistenceCtx).
		Model(&metadataJobRecord{}).
		Where("id = ? AND status = ? AND started_at = ?", job.ID, metadataAIJobStatusRunning, startedAt).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("metadata AI job failed with %q and the failure state could not be stored: %w", message, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	job.Status = metadataAIJobStatusFailed
	job.Error = &message
	job.DurationMS = &durationMS
	job.CompletedAt = &finishedAt
	job.UpdatedAt = finishedAt

	slog.Warn("Metadata AI job failed",
		"job_id", job.ID,
		"target_type", job.TargetType,
		"target_id", job.TargetID,
		"duration_ms", durationMS,
		"error", message,
	)

	return nil
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

func parseMetadataSuggestionPayload(
	text string,
	requestedKeys []string,
) (map[string]string, error) {
	objectText := extractJSONObjectText(text)
	var raw structured.Value
	if err := json.Unmarshal([]byte(objectText), &raw); err != nil {
		return nil, fmt.Errorf("metadata AI response is not valid JSON: %w", err)
	}

	unwrapped := unwrapMetadataSuggestionObject(raw)
	record, ok := unwrapped.(structured.Fields)
	if !ok {
		return nil, fmt.Errorf("metadata AI response must be a JSON object")
	}

	allowed := map[string]struct{}{}
	for _, key := range requestedKeys {
		if _, ok := metadataSuggestionResponseKeys[key]; ok {
			allowed[key] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("metadata AI response requested_keys are empty")
	}

	normalized := map[string]string{}
	for rawKey, value := range record {
		normalizedKey := normalizeMetadataSuggestionKey(rawKey)
		if _, ok := allowed[normalizedKey]; !ok {
			continue
		}
		stringValue, ok := value.(string)
		if !ok {
			continue
		}
		stringValue = strings.TrimSpace(stringValue)
		if stringValue == "" {
			continue
		}
		normalized[normalizedKey] = stringValue
	}

	if len(normalized) == 0 {
		return nil, fmt.Errorf("metadata AI response did not include any requested fields")
	}
	return normalized, nil
}

func extractJSONObjectText(text string) string {
	trimmed := stripCodeFence(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return trimmed
	}

	start := strings.Index(trimmed, "{")
	if start == -1 {
		return trimmed
	}

	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(trimmed); index += 1 {
		char := trimmed[index]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch char {
		case '{':
			depth += 1
		case '}':
			depth -= 1
			if depth == 0 {
				return trimmed[start : index+1]
			}
		}
	}

	return trimmed
}

func stripCodeFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}

	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```JSON")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}

func unwrapMetadataSuggestionObject(value structured.Value) structured.Value {
	record, ok := value.(structured.Fields)
	if !ok {
		return value
	}

	for _, key := range []string{"metadata", "suggestion", "result", "data", "output"} {
		candidate, exists := record[key]
		if !exists {
			continue
		}
		if _, ok := candidate.(structured.Fields); ok {
			return candidate
		}
	}

	return value
}

func normalizeMetadataSuggestionKey(key string) string {
	switch key {
	case "summary":
		return "summary"
	default:
		return key
	}
}

func buildMetadataSuggestionMessage(values map[string]string) *managev1.MetadataSuggestion {
	if len(values) == 0 {
		return nil
	}

	message := &managev1.MetadataSuggestion{}
	if value := strings.TrimSpace(values["summary"]); value != "" {
		message.Summary = &value
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
