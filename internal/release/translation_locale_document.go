package release

import (
	"time"
)

type translationLocaleDocumentSaveInput struct {
	Title               *string
	OverwriteNullFields bool
	Now                 time.Time
}
