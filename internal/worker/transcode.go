package worker

import "context"

func (h *Handlers) HandleTranscodeProgress(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleTranscodeProgress(ctx, body)
}

func (h *Handlers) HandleTranscodeComplete(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleTranscodeComplete(ctx, body)
}

func (h *Handlers) HandleWaveformProgress(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleWaveformProgress(ctx, body)
}

func (h *Handlers) HandleWaveformComplete(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleWaveformComplete(ctx, body)
}

func (h *Handlers) HandleWaveformFail(ctx context.Context, body []byte) error {
	return h.fileMediaRuntime.HandleWaveformFail(ctx, body)
}
