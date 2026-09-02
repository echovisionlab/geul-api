package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type compactJob struct {
	ID           string  `json:"i"`
	Profile      string  `json:"p"`
	Document     string  `json:"d"`
	TargetLocale string  `json:"tl"`
	SourceLocale string  `json:"sl"`
	Status       string  `json:"s"`
	OperationID  string  `json:"o"`
	RequestedAt  *string `json:"rq,omitempty"`
	StartedAt    *string `json:"st,omitempty"`
}

type compactJobCancellation struct {
	JobID string `json:"j"`
}

func translationStructuredResult(encoded []byte, err error) (mcpserver.ToolResult, error) {
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return structuredResult(encoded, false)
}

var translationJobStatuses = map[managev1.TranslationJobStatus]string{
	managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_QUEUED:  "queued",
	managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_RUNNING: "running",
}

var translationJobStatusesByName = func() map[string]managev1.TranslationJobStatus {
	statuses := make(map[string]managev1.TranslationJobStatus, len(translationJobStatuses))
	for status, name := range translationJobStatuses {
		statuses[name] = status
	}
	return statuses
}()

func encodeTranslationJobCancellation(jobID string) ([]byte, error) {
	return json.Marshal(compactJobCancellation{JobID: jobID})
}

func compactTranslationJob(job *managev1.TranslationJob) (compactJob, error) {
	if job == nil || strings.TrimSpace(job.Id) == "" || job.Target == nil || strings.TrimSpace(job.Target.EntityId) == "" {
		return compactJob{}, errors.New("translation application returned an incomplete Job")
	}
	if err := validateUUID("Translation application Job ID", job.Id); err != nil {
		return compactJob{}, err
	}
	if err := validateCompactOpaque("Translation application Job document reference", job.Target.EntityId, 256); err != nil {
		return compactJob{}, err
	}
	if err := validateCompactLocale(core.Locale(job.TargetLocale)); err != nil {
		return compactJob{}, fmt.Errorf("translation application returned invalid target locale: %w", err)
	}
	if err := validateCompactLocale(core.Locale(job.SourceLocale)); err != nil {
		return compactJob{}, fmt.Errorf("translation application returned invalid source locale: %w", err)
	}
	if err := validateUUID("Translation application operation ID", job.OperationId); err != nil {
		return compactJob{}, err
	}
	profile, ok := translationProfile(job.Target.EntityType)
	if !ok {
		return compactJob{}, fmt.Errorf("translation application returned unsupported Job target %q", job.Target.EntityType)
	}
	status, ok := translationJobStatuses[job.Status]
	if !ok {
		return compactJob{}, fmt.Errorf("translation application returned unsupported Job status %q", job.Status)
	}
	result := compactJob{
		ID: job.Id, Profile: string(profile), Document: job.Target.EntityId,
		TargetLocale: job.TargetLocale, SourceLocale: job.SourceLocale, Status: status,
		OperationID: job.OperationId,
	}
	var err error
	if result.RequestedAt, err = compactTimestamp(job.RequestedAt); err != nil {
		return compactJob{}, err
	}
	if result.StartedAt, err = compactTimestamp(job.StartedAt); err != nil {
		return compactJob{}, err
	}
	return result, nil
}

func compactTimestamp(value *timestamppb.Timestamp) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.CheckValid(); err != nil {
		return nil, fmt.Errorf("translation application returned an invalid timestamp: %w", err)
	}
	formatted := value.AsTime().UTC().Format(time.RFC3339Nano)
	return &formatted, nil
}

func compactTranslationJobs(jobs []*managev1.TranslationJob) ([]compactJob, error) {
	compact := make([]compactJob, 0, len(jobs))
	for _, job := range jobs {
		converted, err := compactTranslationJob(job)
		if err != nil {
			return nil, err
		}
		compact = append(compact, converted)
	}
	return compact, nil
}

func encodeTranslationJobs(jobs []*managev1.TranslationJob) ([]byte, error) {
	compact, err := compactTranslationJobs(jobs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"j": compact})
}

func encodeTranslationJobList(jobs []*managev1.TranslationJob, pagination *commonv1.PaginationResponse) ([]byte, error) {
	compact, err := compactTranslationJobs(jobs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"j": compact,
		"g": [4]any{pagination.Total, pagination.Limit, pagination.Offset, pagination.HasMore},
	})
}

func encodeTranslationEntries(target *managev1.TranslationTarget, sourceLocale string, entries []*managev1.TranslationEntry) ([]byte, error) {
	if err := validateCompactLocale(core.Locale(sourceLocale)); err != nil {
		return nil, fmt.Errorf("translation application returned invalid source locale: %w", err)
	}
	compact := make([][2]any, 0, len(entries))
	for _, entry := range entries {
		metadata, err := compactTranslationEntry(target, entry)
		if err != nil {
			return nil, err
		}
		compact = append(compact, [2]any{metadata.Locale, metadata.UpdatedAt})
	}
	return json.Marshal(map[string]any{"s": sourceLocale, "e": compact})
}

type compactEntry struct {
	Profile   string  `json:"p"`
	Document  string  `json:"d"`
	Locale    string  `json:"l"`
	UpdatedAt *string `json:"u,omitempty"`
}

func compactTranslationEntry(expected *managev1.TranslationTarget, entry *managev1.TranslationEntry) (compactEntry, error) {
	if entry == nil || entry.Target == nil || expected == nil ||
		entry.Target.EntityType != expected.EntityType || entry.Target.EntityId != expected.EntityId {
		return compactEntry{}, errors.New("translation application returned a different locale target")
	}
	profile, ok := translationProfile(entry.Target.EntityType)
	if !ok {
		return compactEntry{}, errors.New("translation application returned an unsupported locale target")
	}
	if err := validateCompactLocale(core.Locale(entry.Locale)); err != nil {
		return compactEntry{}, err
	}
	updatedAt, err := compactTimestamp(entry.UpdatedAt)
	if err != nil {
		return compactEntry{}, err
	}
	return compactEntry{Profile: string(profile), Document: entry.Target.EntityId, Locale: entry.Locale, UpdatedAt: updatedAt}, nil
}

func encodeTranslationEntry(expected *managev1.TranslationTarget, entry *managev1.TranslationEntry) ([]byte, error) {
	metadata, err := compactTranslationEntry(expected, entry)
	if err != nil {
		return nil, err
	}
	return json.Marshal(metadata)
}
