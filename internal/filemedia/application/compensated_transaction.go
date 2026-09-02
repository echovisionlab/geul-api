package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"gorm.io/gorm"
)

const transactionCompensationTimeout = 10 * time.Second

type registerTransactionCompensation func(time.Time, func(context.Context) error) error

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

	var (
		compensation       func(context.Context) error
		commitStarted      bool
		spiceDBConfirmedAt time.Time
	)
	register := func(confirmedAt time.Time, next func(context.Context) error) error {
		if next == nil {
			return fmt.Errorf("file deletion transaction compensation is required")
		}
		if confirmedAt.IsZero() {
			return fmt.Errorf("file authorization write confirmation time is required")
		}
		if compensation != nil {
			return fmt.Errorf("file deletion transaction compensation may be registered only once")
		}
		compensation = next
		spiceDBConfirmedAt = confirmedAt
		return nil
	}
	compensate := func() error {
		if compensation == nil {
			return nil
		}
		compensationCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			transactionCompensationTimeout,
		)
		defer cancel()
		if err := compensation(compensationCtx); err != nil {
			authzmutation.RecordAuthorizationRollbackCompensationFailed(compensationCtx)
			return fmt.Errorf("compensate file deletion transaction: %w", err)
		}
		return nil
	}
	rollback := func() error {
		if err := tx.Rollback().Error; err != nil {
			return fmt.Errorf("rollback file deletion transaction: %w", err)
		}
		return nil
	}

	defer func() {
		panicValue := recover()
		if panicValue == nil {
			return
		}
		if commitStarted {
			if compensation != nil {
				authzmutation.RecordAuthorizationCommitUncertain(
					context.WithoutCancel(ctx),
					spiceDBConfirmedAt,
				)
			}
			panic(errors.Join(
				transactionPanicError(panicValue),
				fmt.Errorf("file deletion transaction commit outcome is uncertain; rollback and compensation were not attempted"),
			))
		}
		panicErr := transactionPanicError(panicValue)
		joined := false
		if compensationErr := compensate(); compensationErr != nil {
			panicErr = errors.Join(panicErr, compensationErr)
			joined = true
		}
		if rollbackErr := rollback(); rollbackErr != nil {
			panicErr = errors.Join(panicErr, rollbackErr)
			joined = true
		}
		if !joined {
			panic(panicValue)
		}
		panic(panicErr)
	}()

	if callbackErr := callback(tx, register); callbackErr != nil {
		var result error = callbackErr
		if compensationErr := compensate(); compensationErr != nil {
			result = errors.Join(result, compensationErr)
		}
		if rollbackErr := rollback(); rollbackErr != nil {
			result = errors.Join(result, rollbackErr)
		}
		return result
	}

	commitStarted = true
	if commitErr := tx.Commit().Error; commitErr != nil {
		if compensation != nil {
			authzmutation.RecordAuthorizationCommitUncertain(
				context.WithoutCancel(ctx),
				spiceDBConfirmedAt,
			)
		}
		return fmt.Errorf("commit file deletion transaction: %w", commitErr)
	}
	if compensation != nil {
		authzmutation.RecordAuthorizationCommitSucceeded(
			context.WithoutCancel(ctx),
			spiceDBConfirmedAt,
		)
	}
	return nil
}

func transactionPanicError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("file deletion transaction panic: %v", value)
}
