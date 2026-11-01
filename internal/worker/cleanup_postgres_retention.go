package worker

import (
	"context"
	"log/slog"
	"time"
)

const authCodeIssuanceCleanupLimit = 1000

func (h *Handlers) handleCleanupAuthCodeIssuance(ctx context.Context, now time.Time) error {
	deleted, err := h.authCodes.CleanupExpired(ctx, now, authCodeIssuanceCleanupLimit)
	if err != nil {
		return err
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "Cleaned expired auth code issuance rows", "count", deleted)
	}
	return nil
}

func (h *Handlers) handleCleanupPGMQArchives(ctx context.Context) error {
	purged, err := h.queueArchives.PurgeExpired(ctx)
	if err != nil {
		return err
	}
	for _, purge := range purged {
		if purge.Deleted > 0 {
			slog.InfoContext(ctx, "Purged expired PGMQ archive rows", "queue", purge.Queue, "count", purge.Deleted)
		}
	}
	return nil
}
