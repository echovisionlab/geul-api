package legal

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// ApplyTypedTranslationCandidateWithDB persists one translated policy locale
// behind the archived-policy translation fence, in the Translation
// application's transaction. The Translation application reads its answers
// as Connect errors; a candidate whose source moved stays the bare
// translation.ErrSourceNoLongerCurrent it discards.
func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	auditWriter domainaudit.Appender,
) error {
	err := applyTypedTranslationCandidate(ctx, tx, store, job, candidate, auditWriter)
	if err == nil || errors.Is(err, translation.ErrSourceNoLongerCurrent) {
		return err
	}
	return errs.Wrap(err)
}
