package workerpostgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/worker"
)

type shareLinks struct{ db *gorm.DB }

func (s shareLinks) DeleteExpiredLinks(ctx context.Context, now time.Time) (int64, error) {
	return sharelinkadapter.DeleteExpiredLinks(ctx, s.db, now)
}

type authCodeIssuances struct{ db *gorm.DB }

func (a authCodeIssuances) CleanupExpired(ctx context.Context, now time.Time, limit int) (int64, error) {
	return authentication.CleanupExpiredAuthCodeIssuances(ctx, a.db, now, limit)
}

// queueArchives purges through public.purge_pgmq_archives(), which knows
// each queue's archive retention.
type queueArchives struct{ db *gorm.DB }

func (q queueArchives) PurgeExpired(ctx context.Context) ([]worker.QueueArchivePurge, error) {
	var results []struct {
		QueueName    string `gorm:"column:queue_name"`
		DeletedCount int64  `gorm:"column:deleted_count"`
	}
	if err := q.db.WithContext(ctx).
		Raw("SELECT queue_name, deleted_count FROM public.purge_pgmq_archives()").
		Scan(&results).Error; err != nil {
		return nil, err
	}
	purged := make([]worker.QueueArchivePurge, 0, len(results))
	for _, result := range results {
		purged = append(purged, worker.QueueArchivePurge{Queue: result.QueueName, Deleted: result.DeletedCount})
	}
	return purged, nil
}
