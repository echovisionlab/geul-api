package worker

import (
	"context"
	"log/slog"
	"time"
)

func (h *Handlers) handleCleanupStaleMeshOptimizationCandidates(ctx context.Context, now time.Time) error {
	if h.fileMediaRuntime == nil {
		return nil
	}
	count, err := h.fileMediaRuntime.ExpireStaleMeshOptimizationCandidates(ctx, now)
	if err != nil {
		return err
	}
	if count > 0 {
		slog.Info("Cleaned up stale mesh optimization candidates", "count", count)
	}
	return nil
}
