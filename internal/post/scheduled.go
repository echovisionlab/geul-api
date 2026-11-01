package post

import (
	"context"
	"time"
)

const defaultScheduledPostBatchSize = 100

// ProcessDueScheduledPosts publishes due Posts from their current authoritative
// rows in store, bound to the scheduler job's transaction: the job has no
// request principal, and the schedule an authorized SchedulePost stored is its
// authority. SKIP LOCKED and the status predicate make concurrent/redelivered
// ticks duplicate-safe; a missed wake-up is recovered by the next minute scan.
func ProcessDueScheduledPosts(ctx context.Context, store Store, limit int) ([]string, error) {
	if limit <= 0 {
		limit = defaultScheduledPostBatchSize
	}
	publishedIDs := make([]string, 0, limit)
	due, err := store.LockDuePosts(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range due {
		now := time.Now().UTC()
		var publishedAt *time.Time
		if due[i].PublishedAt == nil {
			publishedAt = &now
		}
		published, err := store.PublishDuePost(ctx, due[i].ID, publishedAt, now)
		if err != nil {
			return nil, err
		}
		if published {
			publishedIDs = append(publishedIDs, due[i].ID)
		}
	}
	return publishedIDs, nil
}
