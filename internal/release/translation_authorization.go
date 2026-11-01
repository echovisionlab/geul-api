package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
)

// RequireLockedSourceLocaleEdit locks the Release root before checking the
// same authority so source-locale changes serialize with Release mutations,
// in the translation domain's transaction store is bound to. lock locks the
// principal's Identity in that transaction.
func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	store Store,
	lock func(*auth.UserInfo) (bool, error),
	checker CollaborationPermissionChecker,
	releaseID string,
) error {
	return requireLockedReleaseAction(ctx, store, lock, checker, releaseID, releaseActionEdit)
}
