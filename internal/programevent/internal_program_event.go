package programevent

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// applyProgramEventLocaleMetadataMutation writes the source locale's title
// and summary under the locked event and reports whether it changed them
// and whether the change affects the translation source.
func applyProgramEventLocaleMetadataMutation(
	ctx context.Context,
	store Store,
	eventID string,
	locale string,
	title *string,
	patchTitle bool,
	summary *string,
	patchSummary bool,
	now time.Time,
) (bool, bool, error) {
	rootTitle, rootSourceLocale, err := store.LockTitle(ctx, eventID)
	if err != nil {
		return false, false, err
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, store, eventID, true)
	if err != nil {
		return false, false, err
	}
	if rootSourceLocale != sourceState.SourceLocale {
		return false, false, domainerr.Precondition("Program Event source locale changed; reload before saving")
	}
	if locale != sourceState.SourceLocale {
		return false, false, domainerr.Precondition("target Program Event locale metadata is read-only")
	}
	localeRow, err := store.LockLocale(ctx, eventID, locale)
	if err != nil {
		return false, false, err
	}
	changed := false
	if patchTitle {
		if title == nil || strings.TrimSpace(*title) == "" {
			return false, false, domainerr.Required("title")
		}
		normalized := strings.TrimSpace(*title)
		if normalized != rootTitle {
			if err := store.SetTitle(ctx, eventID, normalized, now); err != nil {
				return false, false, err
			}
			changed = true
		}
	}
	if patchSummary && !sameNullableString(localeRow.Summary, summary) {
		if err := store.SetLocaleSummary(ctx, eventID, locale, summary, now); err != nil {
			return false, false, err
		}
		changed = true
	}
	return changed, changed && locale == sourceState.SourceLocale, nil
}
