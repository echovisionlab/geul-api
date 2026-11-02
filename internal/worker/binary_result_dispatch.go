package worker

import (
	"context"

	filemediaruntime "github.com/echovisionlab/geul-api/internal/adapters/filemedia/runtime"
	"github.com/echovisionlab/geul-api/internal/mq"
)

func (h *Handlers) handleTranscodeResultMessage(ctx context.Context, msg mq.Message) error {
	result, err := filemediaruntime.DecodeTranscodeResult(msg.Body)
	if err != nil {
		return terminalQueueContractError("invalid_transcode_result", err)
	}
	if err := requireStableDeliveryMessageID(msg, result.StableID); err != nil {
		return err
	}
	return h.files.HandleTranscodeComplete(ctx, result.Body)
}

func (h *Handlers) handleWaveformResultMessage(ctx context.Context, msg mq.Message) error {
	result, err := filemediaruntime.DecodeWaveformResult(msg.Body)
	if err != nil {
		return terminalQueueContractError("invalid_waveform_result", err)
	}
	if err := requireStableDeliveryMessageID(msg, result.StableID); err != nil {
		return err
	}
	if result.Complete {
		return h.files.HandleWaveformComplete(ctx, result.Body)
	}
	return h.files.HandleWaveformFail(ctx, result.Body)
}

func (h *Handlers) handleMeshOptimizationResultMessage(ctx context.Context, msg mq.Message) error {
	result, err := filemediaruntime.DecodeMeshOptimizationResult(msg.Body)
	if err != nil {
		return terminalQueueContractError("invalid_mesh_result", err)
	}
	if err := requireStableDeliveryMessageID(msg, result.StableID); err != nil {
		return err
	}
	if result.Complete {
		return h.files.HandleMeshOptimizationComplete(ctx, result.Body)
	}
	return h.files.HandleMeshOptimizationFail(ctx, result.Body)
}

// HandleTranscodeProgress handles a transcode progress broadcast.
func (h *Handlers) HandleTranscodeProgress(ctx context.Context, body []byte) error {
	return h.files.HandleTranscodeProgress(ctx, body)
}

// HandleWaveformProgress handles a waveform progress broadcast.
func (h *Handlers) HandleWaveformProgress(ctx context.Context, body []byte) error {
	return h.files.HandleWaveformProgress(ctx, body)
}

// HandleMeshOptimizationProgress handles a mesh optimization progress
// broadcast.
func (h *Handlers) HandleMeshOptimizationProgress(ctx context.Context, body []byte) error {
	return h.files.HandleMeshOptimizationProgress(ctx, body)
}
