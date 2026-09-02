package contentblock

import (
	"context"

	"gorm.io/gorm"
)

func isSharedPresentationBatch(batch Batch) bool {
	return batch.validatedProfile != "" && len(batch.Upserts) > 0 &&
		len(batch.validatedBaseReferences) == len(batch.Upserts) &&
		len(batch.Deletes) == 0 && len(batch.Reorders) == 0 && len(batch.LocaleGroups) == 0
}

// applySharedPresentationPostgres handles existing, placement-preserving base
// edits using only the affected Blocks and their locale rows. Creation, kind
// changes, and structural edits return applicable=false for the scoped cold
// path that owns those larger invariants.
func (s *Store) applySharedPresentationPostgres(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	domain DomainContext,
) (Result, bool, error) {
	return s.applySharedCTEPostgres(ctx, tx, batch, domain)
}
