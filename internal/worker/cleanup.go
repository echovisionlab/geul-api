package worker

import (
	"context"
	stderrors "errors"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	mediaassetdomain "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
)

func (h *Handlers) mediaAssetCleanup() *mediaassetdomain.Cleanup {
	return h.mediaCleanup
}

func (h *Handlers) publicAssetCleanup() *mediaassetdomain.PublicAssetCleanup {
	return h.publicAssets
}

func (h *Handlers) handleCleanupDanglingFiles(ctx context.Context) error {
	now := time.Now().UTC()
	mediaErr := h.mediaAssetCleanup().CleanupDangling(ctx, now)
	meshErr := h.handleCleanupStaleMeshOptimizationCandidates(ctx, now)
	return stderrors.Join(mediaErr, meshErr)
}

func (h *Handlers) handleCleanupShareLinks(ctx context.Context) error {
	result := h.db.WithContext(ctx).Where("expires_at < ?", time.Now()).Delete(&model.ShareLink{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		slog.Info("Cleaned up expired share links", "count", result.RowsAffected)
	}
	return nil
}

func (h *Handlers) handleCleanupPublicAssets(ctx context.Context, now time.Time) error {
	now = now.UTC()
	unboundOgErr := h.ogCleanup.MarkUnboundReadyAssets(ctx, now.Add(-og.UnboundAssetRetention), now)
	faviconErr := h.faviconCleanup.MarkDanglingReadyAssets(ctx, now.Add(-favicon.DanglingAssetRetention), now)
	terminalOgErr := h.ogCleanup.MarkExpiredTerminalGenerationAssets(ctx, now.Add(-og.TerminalAssetRetention), now)
	pendingErr := h.publicAssetCleanup().DeletePending(ctx, now)
	unreadyErr := h.publicAssetCleanup().DeleteExpiredUnready(ctx, now.Add(-mediaassetdomain.UnreadyPublicAssetRetention))
	return stderrors.Join(unboundOgErr, faviconErr, terminalOgErr, pendingErr, unreadyErr)
}
