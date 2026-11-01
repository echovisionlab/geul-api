package ai

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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
		Target: req.Msg.GetTarget(), Context: req.Msg.GetContext(), Prompt: req.Msg.GetPrompt(),
	})
	return respond(&managev1.StartMetadataGenerationResponse{Job: job}, err)
}

func (h *ConnectHandler) GetMetadataGenerationJob(ctx context.Context, req *connect.Request[managev1.GetMetadataGenerationJobRequest]) (*connect.Response[managev1.GetMetadataGenerationJobResponse], error) {
	job, err := h.metadata.GetMetadataGenerationJob(ctx, GetMetadataGenerationJobQuery{JobID: req.Msg.GetJobId()})
	return respond(&managev1.GetMetadataGenerationJobResponse{Job: job}, err)
}

func (h *ConnectHandler) ResolveMetadataGenerationJob(ctx context.Context, req *connect.Request[managev1.ResolveMetadataGenerationJobRequest]) (*connect.Response[managev1.ResolveMetadataGenerationJobResponse], error) {
	job, err := h.metadata.ResolveMetadataGenerationJob(ctx, ResolveMetadataGenerationJobCommand{
		JobID: req.Msg.GetJobId(), Resolution: req.Msg.GetResolution(),
	})
	return respond(&managev1.ResolveMetadataGenerationJobResponse{Job: job}, err)
}
