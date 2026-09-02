//go:build integration

package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// PurgePGMQQueue removes visible and in-flight test messages while preserving
// the queue declared by the canonical schema SQL.
func PurgePGMQQueue(ctx context.Context, db *sql.DB, queue string) error {
	if db == nil {
		return fmt.Errorf("PostgreSQL connection is required")
	}
	var purged int64
	if err := db.QueryRowContext(ctx, "SELECT pgmq.purge_queue($1)", queue).Scan(&purged); err != nil {
		return fmt.Errorf("purge PGMQ queue %s: %w", queue, err)
	}
	return nil
}

// ReadPGMQ reads up to batch messages and leaves them hidden for the supplied
// visibility timeout. Tests complete or retry each returned message explicitly.
func ReadPGMQ(
	ctx context.Context,
	db *sql.DB,
	queue string,
	visibilityTimeout time.Duration,
	batch int,
) ([]eventpkg.Message, error) {
	return (eventpkg.PGMQ{}).Read(ctx, db, queue, visibilityTimeout, batch)
}

func CompletePGMQ(ctx context.Context, db *sql.DB, queue string, transportID int64) error {
	return (eventpkg.PGMQ{}).Complete(ctx, db, queue, transportID)
}
