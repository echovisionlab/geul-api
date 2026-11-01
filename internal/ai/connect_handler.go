package ai

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the metadata-generation RPCs of AIService by
// translating each request into a use-case command or query.
type ConnectHandler struct {
	metadata *Service
}

var _ managev1connect.AIServiceHandler = (*ConnectHandler)(nil)

// NewConnectHandler returns the Connect adapter of metadata.
func NewConnectHandler(metadata *Service) *ConnectHandler {
	return &ConnectHandler{metadata: metadata}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) StartMetadataGeneration(ctx context.Context, req *connect.Request[managev1.StartMetadataGenerationRequest]) (*connect.Response[managev1.StartMetadataGenerationResponse], error) {
	job, err := h.metadata.StartMetadataGeneration(ctx, StartMetadataGenerationCommand{
		Target: targetFromMessage(req.Msg.GetTarget()), Context: req.Msg.GetContext(), Prompt: req.Msg.GetPrompt(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.StartMetadataGenerationResponse{Job: jobMessage(job)}, nil)
}

func (h *ConnectHandler) GetMetadataGenerationJob(ctx context.Context, req *connect.Request[managev1.GetMetadataGenerationJobRequest]) (*connect.Response[managev1.GetMetadataGenerationJobResponse], error) {
	job, err := h.metadata.GetMetadataGenerationJob(ctx, GetMetadataGenerationJobQuery{JobID: req.Msg.GetJobId()})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.GetMetadataGenerationJobResponse{Job: jobMessage(job)}, nil)
}

func (h *ConnectHandler) ResolveMetadataGenerationJob(ctx context.Context, req *connect.Request[managev1.ResolveMetadataGenerationJobRequest]) (*connect.Response[managev1.ResolveMetadataGenerationJobResponse], error) {
	job, err := h.metadata.ResolveMetadataGenerationJob(ctx, ResolveMetadataGenerationJobCommand{
		JobID: req.Msg.GetJobId(), Resolution: resolutionFromMessage(req.Msg.GetResolution()),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&managev1.ResolveMetadataGenerationJobResponse{Job: jobMessage(job)}, nil)
}

// targetFromMessage keeps the resource type as its enum name, which is how
// a job stores it.
func targetFromMessage(target *managev1.AIResourceTarget) *Target {
	if target == nil {
		return nil
	}
	return &Target{Type: target.Type.String(), ID: target.Id}
}

func resolutionFromMessage(resolution managev1.MetadataGenerationJobResolution) Resolution {
	switch resolution {
	case managev1.MetadataGenerationJobResolution_METADATA_GENERATION_JOB_RESOLUTION_APPLIED:
		return ResolutionApplied
	case managev1.MetadataGenerationJobResolution_METADATA_GENERATION_JOB_RESOLUTION_DISMISSED:
		return ResolutionDismissed
	default:
		return ""
	}
}

// suggestionFields maps each registered suggestion key to its field of the
// wire message.
var suggestionFields = map[string]func(*managev1.MetadataSuggestion, string){
	metadataSuggestionKeySummary: func(message *managev1.MetadataSuggestion, value string) {
		message.Summary = &value
	},
}

func suggestionMessage(values map[string]string) *managev1.MetadataSuggestion {
	message := &managev1.MetadataSuggestion{}
	for key, value := range values {
		setField, ok := suggestionFields[key]
		if !ok {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			setField(message, value)
		}
	}
	return message
}

// jobMessage is the wire form of a job.
func jobMessage(job Job) *managev1.MetadataGenerationJob {
	message := &managev1.MetadataGenerationJob{
		Id:                job.ID,
		Target:            &managev1.AIResourceTarget{Type: resolveAIResourceType(job.TargetType), Id: job.TargetID},
		RequesterMemberId: job.RequesterMemberID,
		Status:            resolveMetadataAIJobStatus(job.Status),
		RequestedKeys:     append([]string(nil), job.RequestedKeys...),
		CreatedAt:         timestamppb.New(job.CreatedAt),
		UpdatedAt:         timestamppb.New(job.UpdatedAt),
	}
	if len(job.Suggestion) > 0 {
		message.Suggestion = suggestionMessage(job.Suggestion)
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

func resolveMetadataAIJobStatus(status JobStatus) managev1.MetadataGenerationJobStatus {
	switch status {
	case JobQueued:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_QUEUED
	case JobRunning:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_RUNNING
	case JobReady:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_READY
	case JobFailed:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_FAILED
	case JobApplied:
		return managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_APPLIED
	case JobDismissed:
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
