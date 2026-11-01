package filemediapostgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/application"
	"gorm.io/gorm"
)

const transactionCompensationTimeout = 10 * time.Second

type registerTransactionCompensation = application.RegisterCompensation

func executeCompensatedTransaction(
	ctx context.Context,
	db *gorm.DB,
	callback func(*gorm.DB, registerTransactionCompensation) error,
) error {
	if db == nil {
		return fmt.Errorf("file deletion transaction database is required")
	}
	if callback == nil {
		return fmt.Errorf("file deletion transaction callback is required")
	}

	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("begin file deletion transaction: %w", tx.Error)
	}
	transaction := &compensatedTransaction{ctx: ctx, tx: tx}
	defer func() {
		if panicValue := recover(); panicValue != nil {
			transaction.repanic(panicValue)
		}
	}()

	if callbackErr := callback(tx, transaction.register); callbackErr != nil {
		result := callbackErr
		if compensationErr := transaction.compensate(); compensationErr != nil {
			result = errors.Join(result, compensationErr)
		}
		if rollbackErr := transaction.rollback(); rollbackErr != nil {
			result = errors.Join(result, rollbackErr)
		}
		return result
	}

	transaction.commitStarted = true
	if commitErr := tx.Commit().Error; commitErr != nil {
		if transaction.compensation != nil {
			authzmutation.RecordAuthorizationCommitUncertain(
				context.WithoutCancel(ctx),
				transaction.spiceDBConfirmedAt,
			)
		}
		return fmt.Errorf("commit file deletion transaction: %w", commitErr)
	}
	if transaction.compensation != nil {
		authzmutation.RecordAuthorizationCommitSucceeded(
			context.WithoutCancel(ctx),
			transaction.spiceDBConfirmedAt,
		)
	}
	return nil
}

// compensatedTransaction is one file deletion transaction and the single
// authorization compensation its callback may register.
type compensatedTransaction struct {
	ctx                context.Context
	tx                 *gorm.DB
	compensation       func(context.Context) error
	commitStarted      bool
	spiceDBConfirmedAt time.Time
}

func (t *compensatedTransaction) register(confirmedAt time.Time, next func(context.Context) error) error {
	if next == nil {
		return fmt.Errorf("file deletion transaction compensation is required")
	}
	if confirmedAt.IsZero() {
		return fmt.Errorf("file authorization write confirmation time is required")
	}
	if t.compensation != nil {
		return fmt.Errorf("file deletion transaction compensation may be registered only once")
	}
	t.compensation = next
	t.spiceDBConfirmedAt = confirmedAt
	return nil
}

func (t *compensatedTransaction) compensate() error {
	if t.compensation == nil {
		return nil
	}
	compensationCtx, cancel := context.WithTimeout(
		context.WithoutCancel(t.ctx),
		transactionCompensationTimeout,
	)
	defer cancel()
	if err := t.compensation(compensationCtx); err != nil {
		authzmutation.RecordAuthorizationRollbackCompensationFailed(compensationCtx)
		return fmt.Errorf("compensate file deletion transaction: %w", err)
	}
	return nil
}

func (t *compensatedTransaction) rollback() error {
	if err := t.tx.Rollback().Error; err != nil {
		return fmt.Errorf("rollback file deletion transaction: %w", err)
	}
	return nil
}

// repanic compensates and rolls back a transaction whose callback panicked,
// then panics again with every failure joined. A panic after the commit began
// leaves the outcome uncertain, so nothing is undone.
func (t *compensatedTransaction) repanic(panicValue any) {
	cause, stack := failurecause.Panic(panicValue)
	slog.ErrorContext(t.ctx, "File deletion transaction panicked", "failure_cause", cause, "cause", cause, "panic_stack", stack)
	if t.commitStarted {
		if t.compensation != nil {
			authzmutation.RecordAuthorizationCommitUncertain(
				context.WithoutCancel(t.ctx),
				t.spiceDBConfirmedAt,
			)
		}
		panic(errors.Join(
			transactionPanicError(panicValue),
			fmt.Errorf("file deletion transaction commit outcome is uncertain; rollback and compensation were not attempted"),
		))
	}
	panicErr := transactionPanicError(panicValue)
	joined := false
	if compensationErr := t.compensate(); compensationErr != nil {
		slog.ErrorContext(t.ctx, "Failed to compensate panicked file deletion transaction", failurecause.Attr(compensationErr))
		panicErr = errors.Join(panicErr, compensationErr)
		joined = true
	}
	if rollbackErr := t.rollback(); rollbackErr != nil {
		slog.ErrorContext(t.ctx, "Failed to rollback panicked file deletion transaction", failurecause.Attr(rollbackErr))
		panicErr = errors.Join(panicErr, rollbackErr)
		joined = true
	}
	if !joined {
		panic(panicValue)
	}
	panic(panicErr)
}

func transactionPanicError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("file deletion transaction panic: %v", value)
}
