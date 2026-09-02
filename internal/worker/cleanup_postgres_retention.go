package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/authentication"
)

const authCodeIssuanceCleanupLimit = 1000

func (h *Handlers) handleCleanupAuthCodeIssuance(ctx context.Context) error {
	deleted, err := authentication.CleanupExpiredAuthCodeIssuances(ctx, h.db, time.Now(), authCodeIssuanceCleanupLimit)
	if err != nil {
		return err
	}
	if deleted > 0 {
		slog.Info("Cleaned expired auth code issuance rows", "count", deleted)
	}
	return nil
}

func (h *Handlers) handleCleanupPGMQArchives(ctx context.Context) error {
	var results []struct {
		QueueName    string `gorm:"column:queue_name"`
		DeletedCount int64  `gorm:"column:deleted_count"`
	}
	if err := h.db.WithContext(ctx).
		Raw("SELECT queue_name, deleted_count FROM public.purge_pgmq_archives()").
		Scan(&results).Error; err != nil {
		return err
	}
	for _, result := range results {
		if result.DeletedCount > 0 {
			slog.Info("Purged expired PGMQ archive rows", "queue", result.QueueName, "count", result.DeletedCount)
		}
	}
	return nil
}
