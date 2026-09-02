package application

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
)

func (m *TranslationJobManager) handleQueuedDeliveryFailure(
	ctx context.Context,
	job *model.TranslationJob,
	cause error,
) error {
	cause = boundTranslationFailure(cause)
	if translationDeliveryTransportStopped(ctx, cause) {
		return cause
	}
	persistCtx, cancel := translationTerminalCleanupContext(ctx)
	defer cancel()
	if err := m.failQueuedJob(persistCtx, job, cause); err != nil {
		return err
	}
	return mq.NewTerminalDeliveryError("translation_generation_failed", cause)
}

func (m *TranslationJobManager) handleRunningDeliveryFailure(
	ctx context.Context,
	job *model.TranslationJob,
	startedAt time.Time,
	cause error,
) error {
	cause = boundTranslationFailure(cause)
	if translationDeliveryTransportStopped(ctx, cause) {
		return cause
	}
	persistCtx, cancel := translationTerminalCleanupContext(ctx)
	defer cancel()
	if err := m.failJob(persistCtx, job, startedAt, cause); err != nil {
		if errors.Is(err, errTranslationJobNoLongerCurrent) {
			terminal, terminalErr := m.translationDeliveryAlreadyTerminal(persistCtx, job.ID)
			if terminalErr != nil {
				return terminalErr
			}
			if terminal {
				return nil
			}
		}
		return err
	}
	return mq.NewTerminalDeliveryError("translation_generation_failed", cause)
}

// translationDeliveryTransportStopped distinguishes cancellation of the
// worker delivery itself from a timeout returned by a provider-owned child
// operation. Only the former remains running for transport redelivery; a
// provider timeout is a terminal failed attempt.
func translationDeliveryTransportStopped(ctx context.Context, cause error) bool {
	if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return false
	}
	return ctx != nil && ctx.Err() != nil
}

func translationTerminalCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), translationTerminalCleanupTimeout)
}
