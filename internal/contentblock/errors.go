package contentblock

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var (
	ErrDocumentNotFound = errors.New("content document not found")
	ErrInvalidMutation  = errors.New("invalid content block mutation")
	ErrStaleRevision    = errors.New("stale content document revision")
	ErrCrossDocument    = errors.New("content block belongs to another document")
	ErrFileReference    = errors.New("invalid content block file reference")
)

// StaleRevisionError preserves the current aggregate CAS token while
// remaining compatible with errors.Is(err, ErrStaleRevision).
type StaleRevisionError struct {
	CurrentRevision uuid.UUID
}

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("%s: current revision is %s", ErrStaleRevision, e.CurrentRevision)
}

func (e *StaleRevisionError) Is(target error) bool {
	return target == ErrStaleRevision
}

func staleRevision(current uuid.UUID) error {
	return &StaleRevisionError{CurrentRevision: current}
}
