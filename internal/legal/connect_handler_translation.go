package legal

import (
	"context"
	"errors"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// ApplyTypedTranslationCandidate persists one translated policy locale
// behind the archived-policy translation fence, in the Translation
// application's transaction joined binds. The Translation application reads
// its answers as Connect errors; a candidate whose source moved stays the
// bare translation.ErrSourceNoLongerCurrent it discards.
func ApplyTypedTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job TranslationJob,
	candidate *translation.Candidate,
) error {
	err := applyTypedTranslationCandidate(ctx, joined, job, candidate)
	if err == nil || errors.Is(err, translation.ErrSourceNoLongerCurrent) {
		return err
	}
	return errs.Wrap(err)
}
