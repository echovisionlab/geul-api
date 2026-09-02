package post

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const defaultScheduledPostBatchSize = 100

// ProcessDueScheduledPosts publishes due Posts from their current authoritative
// rows. SKIP LOCKED and the status predicate make concurrent/redelivered ticks
// duplicate-safe; a missed wake-up is recovered by the next minute scan.
func ProcessDueScheduledPosts(ctx context.Context, db *gorm.DB, limit int) ([]string, error) {
	if limit <= 0 {
		limit = defaultScheduledPostBatchSize
	}
	publishedIDs := make([]string, 0, limit)
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var due []model.Post
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Select("id", "status", "scheduled_at", "published_at").
			Where("status = ? AND scheduled_at <= CURRENT_TIMESTAMP", managev1.PostStatus_POST_STATUS_SCHEDULED.String()).
			Order("scheduled_at ASC, id ASC").
			Limit(limit).
			Find(&due).Error; err != nil {
			return err
		}
		for i := range due {
			now := time.Now().UTC()
			updates := structured.Fields{
				"status":              managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
				"scheduled_at":        nil,
				"scheduled_time_zone": nil,
				"updated_at":          now,
			}
			if due[i].PublishedAt == nil {
				updates["published_at"] = now
			}
			result := tx.Model(&model.Post{}).
				Where("id = ? AND status = ? AND scheduled_at <= CURRENT_TIMESTAMP", due[i].ID, managev1.PostStatus_POST_STATUS_SCHEDULED.String()).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				publishedIDs = append(publishedIDs, due[i].ID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return publishedIDs, nil
}
