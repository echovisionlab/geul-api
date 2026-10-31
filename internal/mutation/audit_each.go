package mutation

import (
	"context"

	"gorm.io/gorm"
)

// Each appends every record of audits, in order, for one change that alters
// several separately audited aspects, such as a Map Place's metadata and its
// image. It needs at least one record.
func Each(audits ...Audit) Audit {
	return Audit{each: audits}
}

func (r *Runner) appendEach(ctx context.Context, tx *gorm.DB, audits []Audit) error {
	for _, audit := range audits {
		if err := r.appendAudit(ctx, tx, audit); err != nil {
			return err
		}
	}
	return nil
}
