package label

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// saveLabelSourceTitle stores the trimmed title of the Label's source
// locale; a blank title is rejected.
func saveLabelSourceTitle(ctx context.Context, store Store, labelID, locale string, title *string, now time.Time) error {
	if title == nil || strings.TrimSpace(*title) == "" {
		return domainerr.InvalidField("title", "must not be empty")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return store.SaveLocaleTitle(ctx, labelID, locale, strings.TrimSpace(*title), now)
}
