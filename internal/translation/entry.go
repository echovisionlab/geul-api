package translation

import (
	"time"
)

// EntryWrite contains only locale-owned target values. Row existence is the
// target-presence authority; nil values are missing units while non-nil empty
// values are explicit empty translations.
type EntryWrite struct {
	Title       *string
	Summary     *string
	ContentJSON []byte
	ContentHTML *string
	ContentText *string
	Now         time.Time
}
