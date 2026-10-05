package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"gorm.io/gorm"
)

// RequireLockedSourceLocaleEdit locks the Release root before checking the
// same authority so source-locale changes serialize with Release mutations.
func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	releaseID string,
) error {
	_, err := requireLockedReleaseAction(ctx, tx, spiceDB, releaseID, releaseActionEdit)
	return err
}
