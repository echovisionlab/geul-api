package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
)

// handleCleanupDanglingFiles runs both cleanups even when the first fails,
// and returns both failures.
func (h *Handlers) handleCleanupDanglingFiles(ctx context.Context, now time.Time) error {
	mediaErr := h.danglingFiles.CleanupDangling(ctx, now)
	meshErr := h.cleanupStaleMeshOptimizationCandidates(ctx, now)
	return errors.Join(mediaErr, meshErr)
}

func (h *Handlers) cleanupStaleMeshOptimizationCandidates(ctx context.Context, now time.Time) error {
	count, err := h.files.ExpireStaleMeshOptimizationCandidates(ctx, now)
	if err != nil {
		return err
	}
	if count > 0 {
		slog.InfoContext(ctx, "Cleaned up stale mesh optimization candidates", "count", count)
	}
	return nil
}

func (h *Handlers) handleCleanupShareLinks(ctx context.Context, now time.Time) error {
	deleted, err := h.shareLinks.DeleteExpiredLinks(ctx, now)
	if err != nil {
		return err
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "Cleaned up expired share links", "count", deleted)
	}
	return nil
}

// handleCleanupPublicAssets marks the expired OG and favicon assets before
// the public asset cleanup deletes what is pending, so one run deletes what
// it marked. Every step runs even when an earlier one fails.
func (h *Handlers) handleCleanupPublicAssets(ctx context.Context, now time.Time) error {
	now = now.UTC()
	unboundOgErr := h.ogCleanup.MarkUnboundReadyAssets(ctx, now.Add(-og.UnboundAssetRetention), now)
	faviconErr := h.faviconCleanup.MarkDanglingReadyAssets(ctx, now.Add(-favicon.DanglingAssetRetention), now)
	terminalOgErr := h.ogCleanup.MarkExpiredTerminalGenerationAssets(ctx, now.Add(-og.TerminalAssetRetention), now)
	pendingErr := h.publicAssets.DeletePending(ctx, now)
	unreadyErr := h.publicAssets.DeleteExpiredUnready(ctx, now.Add(-mediaasset.UnreadyPublicAssetRetention))
	return errors.Join(unboundOgErr, faviconErr, terminalOgErr, pendingErr, unreadyErr)
}
