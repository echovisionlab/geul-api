package ai

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// RecoverExpiredJobs republishes durable commands only after their queue retry
// window has elapsed and no transport row with the job's stable message ID
// remains in PGMQ. The job timestamp update and enqueue share one transaction.
func (m *MetadataJobManager) RecoverExpiredJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = metadataAIJobRecoveryBatch
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	recoveryBefore := now.Add(-metadataAIJobRecoveryDelay)
	staleBefore := now.Add(-metadataAIJobLeaseDuration)
	queueTable := pq.QuoteIdentifier("q_" + eventpkg.QueueAiMetadataGenerate)
	republished := 0
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []metadataJobRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(
				"(status = ? AND updated_at <= ?) OR (status = ? AND updated_at <= ? AND (started_at IS NULL OR started_at <= ?))",
				metadataAIJobStatusQueued,
				recoveryBefore,
				metadataAIJobStatusRunning,
				recoveryBefore,
				staleBefore,
			).
			Order("updated_at ASC").
			Limit(limit).
			Find(&jobs).Error; err != nil {
			return fmt.Errorf("failed to scan expired metadata AI jobs: %w", err)
		}

		for _, job := range jobs {
			var transportPending bool
			query := "SELECT EXISTS (SELECT 1 FROM pgmq." + queueTable + " WHERE message->>'message_id' = ?)"
			if err := tx.Raw(query, job.ID).Scan(&transportPending).Error; err != nil {
				return fmt.Errorf("failed to inspect metadata AI queue message %s: %w", job.ID, err)
			}

			update := tx.Model(&metadataJobRecord{}).
				Where("id = ? AND status = ? AND updated_at = ?", job.ID, job.Status, job.UpdatedAt)
			if job.Status == metadataAIJobStatusRunning {
				if job.StartedAt == nil {
					update = update.Where("started_at IS NULL")
				} else {
					update = update.Where("started_at = ?", *job.StartedAt)
				}
			}
			result := update.Updates(structured.Fields{"updated_at": now})
			if result.Error != nil {
				return fmt.Errorf("failed to refresh expired metadata AI job %s: %w", job.ID, result.Error)
			}
			if result.RowsAffected == 0 || transportPending {
				continue
			}

			if err := publishDurableProtoInTransaction(
				ctx,
				m.asyncPublisher,
				tx,
				eventpkg.QueueAiMetadataGenerate,
				job.ID,
				&managev1.MetadataGenerationQueueEvent{JobId: job.ID},
			); err != nil {
				return fmt.Errorf("failed to republish expired metadata AI job %s: %w", job.ID, err)
			}
			republished++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if republished > 0 {
		slog.Info("Expired metadata AI jobs republished", "count", republished)
	}
	return republished, nil
}
