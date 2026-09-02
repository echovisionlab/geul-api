package og

import (
	"context"

	"gorm.io/gorm"
)

// NoopGlobalReconciler is the explicit global-generation policy for Geul's
// active OG projections. There are no removed music-domain projections to
// reconcile before collecting the current set of targets.
type NoopGlobalReconciler struct{}

func (NoopGlobalReconciler) ReconcileBeforeGlobalGeneration(context.Context, *gorm.DB, string) error {
	return nil
}

var _ GlobalReconciler = NoopGlobalReconciler{}
