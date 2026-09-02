package contentblock

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestStaleRevisionErrorPreservesSentinelAndCurrentRevision(t *testing.T) {
	current := uuid.New()
	err := staleRevision(current)
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale revision does not preserve sentinel compatibility: %v", err)
	}
	var conflict *StaleRevisionError
	if !errors.As(err, &conflict) || conflict.CurrentRevision != current {
		t.Fatalf("stale revision lost current CAS token: %#v", err)
	}
}
