package postgreslock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

const advisoryUnlockTimeout = 2 * time.Second

// WithAdvisoryLeader runs work only when this process acquires a session-scoped
// PostgreSQL lock. It is suitable for periodic scans where PGMQ remains the
// delivery authority.
func WithAdvisoryLeader(ctx context.Context, db *gorm.DB, lockKey int64, work func(context.Context) error) error {
	if db == nil {
		return fmt.Errorf("advisory leader database is required")
	}
	if work == nil {
		return fmt.Errorf("advisory leader work is required")
	}
	return db.WithContext(ctx).Connection(func(connection *gorm.DB) error {
		var acquired bool
		if err := connection.Raw("SELECT pg_try_advisory_lock(?)", lockKey).Scan(&acquired).Error; err != nil {
			return fmt.Errorf("acquire advisory leader lock: %w", err)
		}
		if !acquired {
			return nil
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), advisoryUnlockTimeout)
			defer cancel()
			if err := Release(unlockCtx, connection, lockKey, "advisory leader"); err != nil {
				slog.Error("Failed to release advisory leader lock", "lock_key", lockKey, "error", err)
			}
		}()
		return work(ctx)
	})
}

// WithBoundedAdvisoryLock serializes one request-owned operation across
// replicas while bounding reserved PostgreSQL connections during external I/O.
func WithBoundedAdvisoryLock(ctx context.Context, db *gorm.DB, lockKey int64, slots chan struct{}, scope string, operation func(*gorm.DB) error) error {
	if db == nil {
		return fmt.Errorf("%s database is required", scope)
	}
	if operation == nil {
		return fmt.Errorf("%s operation is required", scope)
	}
	if db.Dialector.Name() != "postgres" {
		return operation(db.WithContext(ctx))
	}

	releaseSlot, err := AcquireSlot(ctx, slots)
	if err != nil {
		return fmt.Errorf("wait for %s advisory lock capacity: %w", scope, err)
	}
	defer releaseSlot()

	return db.WithContext(ctx).Connection(func(connection *gorm.DB) (returnErr error) {
		if err := connection.Exec("SELECT pg_advisory_lock(?)", lockKey).Error; err != nil {
			return fmt.Errorf("acquire %s advisory lock: %w", scope, err)
		}
		defer func() {
			returnErr = errors.Join(returnErr, Release(ctx, connection, lockKey, scope))
		}()
		return operation(connection)
	})
}

// AcquireSlot obtains one bounded advisory-lock admission slot and returns its
// release function.
func AcquireSlot(ctx context.Context, slots chan struct{}) (func(), error) {
	if slots == nil {
		return nil, fmt.Errorf("advisory lock slots are required")
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
