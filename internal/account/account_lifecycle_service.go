package account

import "time"

const (
	accountLifecycleStateConfirmationPending         = "confirmation_pending"
	accountLifecycleStateScheduled                   = "scheduled"
	accountLifecycleStateRecoveryConfirmationPending = "recovery_confirmation_pending"
	accountLifecycleStateCancelled                   = "cancelled"
	accountLifecycleStateRecovered                   = "recovered"
	accountLifecycleReconcileInterval                = 2 * time.Second
	accountLifecycleReconcileBatchSize               = 50
	accountLifecycleLeaderLockKey                    = int64(0x4745554c41434354) // GEULACCT
)

var accountLifecycleDeletionPendingStates = []string{
	accountLifecycleStateScheduled,
	accountLifecycleStateRecoveryConfirmationPending,
}

// AccountDeletionRequestResult tells whether a deletion was already scheduled
// when the principal asked for it again.
type AccountDeletionRequestResult struct {
	AlreadyScheduled bool
}

// AccountDeletionScheduledResult is when a confirmed deletion takes effect.
type AccountDeletionScheduledResult struct {
	ScheduledAt time.Time
}
