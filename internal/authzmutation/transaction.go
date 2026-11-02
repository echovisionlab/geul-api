package authzmutation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

const authorizationCompensationTimeout = 15 * time.Second

// WriteRelationships applies an exact relationship batch and its rollback inverse.
type WriteRelationships = func(
	apply []policyv1.RelationshipMutation,
	compensate []policyv1.RelationshipMutation,
) error

// RelationshipWriter writes an exact relationship batch against its expected
// pre-state and restores it. *auth.SpiceDBClient is the production writer.
type RelationshipWriter interface {
	ApplyRelationshipsExpecting(ctx context.Context, apply, compensate []policyv1.RelationshipMutation) (auth.ZedToken, error)
	CompensateRelationshipsExpecting(ctx context.Context, restore, expectedCurrent []policyv1.RelationshipMutation) (auth.ZedToken, error)
}

// Execute keeps a product database transaction open while
// its exact SpiceDB relationship change is applied. The callback must invoke
// write exactly once and must supply the exact inverse relationship batch.
func Execute(
	ctx context.Context,
	db *gorm.DB,
	spiceDB RelationshipWriter,
	callback func(*gorm.DB, WriteRelationships) error,
) (auth.ZedToken, error) {
	if db == nil {
		return auth.ZedToken{}, fmt.Errorf("authorization database is required")
	}
	if spiceDB == nil {
		return auth.ZedToken{}, fmt.Errorf("SpiceDB client is required")
	}
	if callback == nil {
		return auth.ZedToken{}, fmt.Errorf("authorization mutation callback is required")
	}

	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return auth.ZedToken{}, fmt.Errorf("begin authorized database transaction: %w", tx.Error)
	}
	if lockErr := LockTransaction(tx); lockErr != nil {
		return auth.ZedToken{}, errors.Join(
			lockErr,
			wrapAuthorizationRollbackError(tx.Rollback().Error, " after authorization mutation lock failure"),
		)
	}

	var (
		token                auth.ZedToken
		forward              []policyv1.RelationshipMutation
		compensation         []policyv1.RelationshipMutation
		compensationRequired bool
		writeCalled          bool
		writeSucceeded       bool
		writeErr             error
		commitStarted        bool
		spiceDBConfirmedAt   time.Time
	)
	compensateWhileLocked := func() error {
		if !compensationRequired {
			return nil
		}
		compensationCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			authorizationCompensationTimeout,
		)
		defer cancel()
		_, err := spiceDB.CompensateRelationshipsExpecting(compensationCtx, compensation, forward)
		if err != nil {
			// The returned error keeps the callback's classification, which
			// can be a client outcome; drifted authority needs an operator.
			slog.ErrorContext(compensationCtx, "Authorization rollback compensation failed", failurecause.Attr(err))
			RecordAuthorizationRollbackCompensationFailed(compensationCtx)
			return fmt.Errorf("compensate authorization relationships before database rollback: %w", err)
		}
		return nil
	}
	recordWriteError := func(err error) error {
		if writeErr == nil {
			writeErr = err
		} else {
			writeErr = errors.Join(writeErr, err)
		}
		return err
	}
	write := func(apply, compensate []policyv1.RelationshipMutation) error {
		if writeCalled {
			return recordWriteError(fmt.Errorf("authorization relationships may be written only once per database transaction"))
		}
		writeCalled = true
		if len(apply) == 0 {
			return recordWriteError(fmt.Errorf("authorization relationship mutations are required"))
		}
		if len(compensate) == 0 {
			return recordWriteError(fmt.Errorf("authorization relationship compensation is required"))
		}

		forward = append([]policyv1.RelationshipMutation(nil), apply...)
		compensation = append([]policyv1.RelationshipMutation(nil), compensate...)
		// A panic during the provider call cannot establish whether the atomic
		// write ran. The exact inverse is safe against the expected pre-state.
		compensationRequired = true
		writtenAt, err := spiceDB.ApplyRelationshipsExpecting(ctx, apply, compensate)
		if err != nil {
			compensationRequired = auth.IsRelationshipWriteOutcomeUncertain(err)
			return recordWriteError(fmt.Errorf("write authorization relationships: %w", err))
		}
		token = writtenAt
		writeSucceeded = true
		spiceDBConfirmedAt = time.Now()
		return nil
	}
	defer func() {
		panicValue := recover()
		if panicValue == nil {
			return
		}
		// The re-raised panic reaches the RPC or queue boundary, which logs it
		// once with the original stack.
		if commitStarted {
			RecordAuthorizationCommitUncertain(
				context.WithoutCancel(ctx),
				spiceDBConfirmedAt,
			)
			panic(errors.Join(
				panicError(panicValue),
				fmt.Errorf("authorized database commit outcome is uncertain; rollback and authorization compensation were not attempted"),
			))
		}
		compensationErr := compensateWhileLocked()
		rollbackErr := tx.Rollback().Error
		if compensationErr != nil || rollbackErr != nil {
			panic(errors.Join(
				panicError(panicValue),
				compensationErr,
				wrapAuthorizationRollbackError(rollbackErr, " after panic"),
			))
		}
		panic(panicValue)
	}()

	callbackErr := callback(tx, write)
	if writeErr != nil && !errors.Is(callbackErr, writeErr) {
		callbackErr = errors.Join(callbackErr, writeErr)
	}
	if callbackErr == nil && !writeSucceeded {
		callbackErr = fmt.Errorf("authorization mutation callback must complete one relationship write")
	}
	if callbackErr != nil {
		compensationErr := compensateWhileLocked()
		rollbackErr := tx.Rollback().Error
		if rollbackErr != nil {
			// Logged here for the same reason as a failed compensation: the
			// request can still end with the callback's client error.
			slog.ErrorContext(context.WithoutCancel(ctx), "Authorized database rollback failed", failurecause.Attr(rollbackErr))
		}
		if compensationErr == nil && rollbackErr == nil {
			return auth.ZedToken{}, callbackErr
		}
		return auth.ZedToken{}, errors.Join(
			callbackErr,
			compensationErr,
			wrapAuthorizationRollbackError(rollbackErr, ""),
		)
	}

	// A commit error can mean the database committed but the client lost the
	// result. Preserve the confirmed SpiceDB write and never compensate here.
	commitStarted = true
	if commitErr := tx.Commit().Error; commitErr != nil {
		RecordAuthorizationCommitUncertain(
			context.WithoutCancel(ctx),
			spiceDBConfirmedAt,
		)
		return auth.ZedToken{}, fmt.Errorf("commit authorized database transaction: %w", commitErr)
	}
	RecordAuthorizationCommitSucceeded(context.WithoutCancel(ctx), spiceDBConfirmedAt)
	return token, nil
}

func wrapAuthorizationRollbackError(err error, suffix string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("rollback authorized database transaction%s: %w", suffix, err)
}

func panicError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("authorized mutation panic: %v", value)
}
