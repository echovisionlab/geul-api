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
	return h.HandleTranscodeComplete(ctx, result.Body)
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
		return h.HandleWaveformComplete(ctx, result.Body)
	}
	return h.HandleWaveformFail(ctx, result.Body)
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
		return h.HandleMeshOptimizationComplete(ctx, result.Body)
	}
	return h.HandleMeshOptimizationFail(ctx, result.Body)
}
