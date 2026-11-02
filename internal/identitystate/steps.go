package identitystate

import (
	"context"

	"gorm.io/gorm"
)

// Step runs work in one transaction of its own on a fenced connection and
// commits it when work returns nil.
type Step func(ctx context.Context, work func(tx *gorm.DB) error) error

// WithMutationSteps holds the fence of WithMutation while work runs and
// hands it the steps of a principal-less flow: each step is a transaction on
// the fenced connection that commits when it returns, so an Identity
// provider or SpiceDB call after it cannot roll it back.
func WithMutationSteps(ctx context.Context, db *gorm.DB, identityID string, work func(ctx context.Context, step Step) error) error {
	return WithMutation(ctx, db, identityID, func(mutationCtx context.Context, connection *gorm.DB) error {
		return work(mutationCtx, func(ctx context.Context, step func(tx *gorm.DB) error) error {
			return connection.WithContext(ctx).Transaction(step)
		})
	})
}
