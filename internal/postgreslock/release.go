package postgreslock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

const boundedUnlockTimeout = 5 * time.Second

// Release unlocks one advisory lock held by a reserved PostgreSQL connection.
// If the lock outcome is unknown, it prevents that connection from returning
// to the reusable pool with an active session lock.
func Release(
	ctx context.Context,
	connection *gorm.DB,
	lockKey int64,
	scope string,
) error {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boundedUnlockTimeout)
	defer cancel()

	var unlocked bool
	unlockErr := connection.WithContext(unlockCtx).
		Raw("SELECT pg_advisory_unlock(?)", lockKey).
		Scan(&unlocked).Error
	if unlockErr == nil {
		if !unlocked {
			return fmt.Errorf("release %s advisory lock: lock was not held", scope)
		}
		return nil
	}

	// Do not return a pooled PostgreSQL connection with an unknown session lock.
	// A detached unlock-all fallback is safe because this request exclusively
	// owns the reserved connection for the duration of the callback.
	fallbackCtx, fallbackCancel := context.WithTimeout(context.WithoutCancel(ctx), boundedUnlockTimeout)
	defer fallbackCancel()
	fallbackErr := connection.WithContext(fallbackCtx).Exec("SELECT pg_advisory_unlock_all()").Error
	if fallbackErr != nil {
		discardErr := discardReservedConnection(connection)
		return errors.Join(
			fmt.Errorf("release %s advisory lock: %w", scope, unlockErr),
			fmt.Errorf("release all advisory locks on reserved connection: %w", fallbackErr),
			discardErr,
		)
	}
	return fmt.Errorf("release %s advisory lock: %w (all reserved-connection locks released)", scope, unlockErr)
}

func discardReservedConnection(connection *gorm.DB) error {
	if connection == nil || connection.Statement == nil {
		return fmt.Errorf("discard reserved advisory-lock connection: connection is required")
	}
	reserved, ok := connection.Statement.ConnPool.(*sql.Conn)
	if !ok || reserved == nil {
		return fmt.Errorf(
			"discard reserved advisory-lock connection: unexpected pool type %T",
			connection.Statement.ConnPool,
		)
	}
	err := reserved.Raw(func(structured.Value) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("discard reserved advisory-lock connection: %w", err)
	}
	return fmt.Errorf("discard reserved advisory-lock connection: driver accepted poisoned connection")
}
