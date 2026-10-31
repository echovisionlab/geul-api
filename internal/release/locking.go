package release

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
)

func lockReleaseForUpdate(ctx context.Context, tx *gorm.DB, releaseID string) (*model.Release, error) {
	var release model.Release
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&release, "id = ?", releaseID).Error; err != nil {
		return nil, err
	}
	return &release, nil
}

// lockReleaseRow is the Lock of a Release mutation: the root row FOR UPDATE,
// answering not found before authorization when it is missing. into, when
// set, receives the locked row.
func lockReleaseRow(releaseID string, into *model.Release) func(context.Context, *gorm.DB) error {
	return func(ctx context.Context, tx *gorm.DB) error {
		locked, err := lockReleaseForUpdate(ctx, tx, releaseID)
		if err != nil {
			return missingAs(err, releaseNotFound(releaseID))
		}
		if into != nil {
			*into = *locked
		}
		return nil
	}
}
