package application

import (
	"context"
	"fmt"
	"time"
)

func publishAppliedTranslationContentUpdated(
	ctx context.Context,
	store JobStore,
	job *Job,
	target AppliedTranslationTarget,
	now time.Time,
) error {
	if !target.Changed {
		return nil
	}
	if job == nil {
		return fmt.Errorf("translation job is required")
	}
	return store.PublishTranslationApplied(ctx, *job, target, now)
}
