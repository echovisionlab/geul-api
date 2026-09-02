package worker

import "context"

func (h *Handlers) HandleMeshOptimizationProgress(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleMeshOptimizationProgress(ctx, body)
}

func (h *Handlers) HandleMeshOptimizationComplete(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleMeshOptimizationComplete(ctx, body)
}

func (h *Handlers) HandleMeshOptimizationFail(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleMeshOptimizationFail(ctx, body)
}
