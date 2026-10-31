package post

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const defaultScheduledPostBatchSize = 100

// ProcessDueScheduledPosts publishes due Posts from their current authoritative
// rows in tx, the scheduler job's transaction: the job has no request
// principal, and the schedule an authorized SchedulePost stored is its
// authority. SKIP LOCKED and the status predicate make concurrent/redelivered
// ticks duplicate-safe; a missed wake-up is recovered by the next minute scan.
func ProcessDueScheduledPosts(ctx context.Context, tx *gorm.DB, limit int) ([]string, error) {
	if limit <= 0 {
		limit = defaultScheduledPostBatchSize
	}
	tx = tx.WithContext(ctx)
	publishedIDs := make([]string, 0, limit)
	var due []model.Post
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
		Select("id", "status", "scheduled_at", "published_at").
		Where("status = ? AND scheduled_at <= CURRENT_TIMESTAMP", string(model.PostStatusScheduled)).
		Order("scheduled_at ASC, id ASC").
		Limit(limit).
		Find(&due).Error; err != nil {
		return nil, err
	}
	for i := range due {
		now := time.Now().UTC()
		updates := structured.Fields{
			"status":              string(model.PostStatusPublished),
			"scheduled_at":        nil,
			"scheduled_time_zone": nil,
			"updated_at":          now,
		}
		if due[i].PublishedAt == nil {
			updates["published_at"] = now
		}
		result := tx.Model(&model.Post{}).
			Where("id = ? AND status = ? AND scheduled_at <= CURRENT_TIMESTAMP", due[i].ID, string(model.PostStatusScheduled)).
			Updates(updates)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			publishedIDs = append(publishedIDs, due[i].ID)
		}
	}
	return publishedIDs, nil
}
