package legal

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
)

// readLocked runs read in one transaction after access, for a read that
// locks its target and authorizes against the locked state. A read changes
// nothing, so it records no audit.
func readLocked(ctx context.Context, runner *mutation.Runner, access mutation.Access, read func(context.Context, *gorm.DB) error) error {
	_, err := mutation.Run(ctx, runner, mutation.Spec[struct{}]{
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) { return struct{}{}, read(ctx, tx) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

// authorizedBefore is the access of a read whose caller was authorized
// before its transaction began, or of a collaboration write that its
// contributor fence authorizes.
var authorizedBefore = mutation.Locked(func(context.Context, *gorm.DB, mutation.Require) error { return nil })
