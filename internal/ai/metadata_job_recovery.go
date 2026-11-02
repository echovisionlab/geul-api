package ai

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// RecoverExpiredJobs republishes durable commands only after their queue retry
// window has elapsed and no transport row with the job's stable message ID
// remains in PGMQ. The job timestamp update and enqueue share one
// transaction.
func (m *MetadataJobManager) RecoverExpiredJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = metadataAIJobRecoveryBatch
	}
	return mutation.Run(ctx, m.mutations, mutation.StoreSpec[int, Store]{
		Access: runnerAccess,
		Change: func(ctx context.Context, store Store) (int, error) {
			return recoverExpiredJobs(ctx, store, limit)
		},
		// Metadata jobs are operational queue rows, not audited content.
		Audit: func(int) mutation.Audit { return mutation.Unchanged() },
	})
}

func recoverExpiredJobs(ctx context.Context, store Store, limit int) (int, error) {
	now := dbtime.Now()
	recoveryBefore := now.Add(-metadataAIJobRecoveryDelay)
	staleBefore := now.Add(-metadataAIJobLeaseDuration)
	jobs, err := store.LockExpiredJobs(ctx, recoveryBefore, staleBefore, limit)
	if err != nil {
		return 0, fmt.Errorf("failed to scan expired metadata AI jobs: %w", err)
	}
	republished := 0
	for _, job := range jobs {
		transportPending, err := store.TransportPending(ctx, job.ID)
		if err != nil {
			return 0, fmt.Errorf("failed to inspect metadata AI queue message %s: %w", job.ID, err)
		}
		touched, err := store.TouchExpiredJob(ctx, job, now)
		if err != nil {
			return 0, fmt.Errorf("failed to refresh expired metadata AI job %s: %w", job.ID, err)
		}
		if !touched || transportPending {
			continue
		}
		if err := store.EnqueueGenerate(ctx, job.ID); err != nil {
			return 0, fmt.Errorf("failed to republish expired metadata AI job %s: %w", job.ID, err)
		}
		republished++
	}
	return republished, nil
}
