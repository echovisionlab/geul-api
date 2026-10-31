package mediaasset

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
)

// LockAttachableFilesForUpdate serializes attachment writers with File
// deletion. Callers must use the provided transaction for their reference
// mutation after this check succeeds.
func LockAttachableFilesForUpdate(ctx context.Context, tx *gorm.DB, fileIDs []string) error {
	if tx == nil {
		return fmt.Errorf("database transaction is required")
	}
	ids := sliceutil.SortedUniqueTrimmed(fileIDs)
	if len(ids) == 0 {
		return nil
	}
	var files []model.File
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "delete_requested_at").
		Where("id IN ?", ids).
		Order("id ASC").
		Find(&files).Error; err != nil {
		return errs.Internal(err)
	}
	if len(files) != len(ids) {
		return errs.FailedPrecondition("one or more files no longer exist")
	}
	for _, file := range files {
		if file.DeleteRequestedAt != nil {
			return errs.FailedPrecondition("file is pending deletion")
		}
	}
	return nil
}
