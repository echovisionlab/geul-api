package authzmutation

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/postgreslock"
	"gorm.io/gorm"
)

const (
	// GlobalLockKey coordinates every PostgreSQL mutation that changes SpiceDB authority.
	GlobalLockKey int64 = 0x4745554c41555448 // GEULAUTH
	lockScope           = "authorization mutation"
)

// LockTransaction serializes every PostgreSQL mutation
// that changes SpiceDB authority. Callers must acquire it before narrower
// resource or identity locks and keep the transaction open through commit or
// exact compensation and rollback.
func LockTransaction(tx *gorm.DB) error {
	if tx == nil {
		return fmt.Errorf("authorization mutation transaction is required")
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if tx.Statement == nil || tx.Statement.ConnPool == nil {
		return fmt.Errorf("authorization mutation transaction connection is required")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return fmt.Errorf("authorization mutation transaction is not active")
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", GlobalLockKey).Error; err != nil {
		return fmt.Errorf("acquire authorization mutation transaction lock: %w", err)
	}
	return nil
}

// LockSession acquires the global authorization lock on a reserved connection.
func LockSession(connection *gorm.DB) error {
	if connection == nil {
		return fmt.Errorf("authorization mutation session connection is required")
	}
	if connection.Dialector.Name() != "postgres" {
		return nil
	}
	if err := connection.Exec("SELECT pg_advisory_lock(?)", GlobalLockKey).Error; err != nil {
		return fmt.Errorf("acquire authorization mutation session lock: %w", err)
	}
	return nil
}

// ReleaseSession releases the global authorization lock from a reserved connection.
func ReleaseSession(ctx context.Context, connection *gorm.DB) error {
	if connection == nil {
		return fmt.Errorf("authorization mutation session connection is required")
	}
	if connection.Dialector.Name() != "postgres" {
		return nil
	}
	return postgreslock.Release(
		ctx,
		connection,
		GlobalLockKey,
		lockScope,
	)
}
