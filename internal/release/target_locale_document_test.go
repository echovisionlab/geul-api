package release

import (
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/google/uuid"
)

func TestValidateReleaseCreditNotesPreservesTargetExplicitEmpty(t *testing.T) {
	creditID := uuid.NewString()
	values := []*CreditNote{{CreditID: creditID, Note: ""}}

	notes, err := validateReleaseCreditNotes(values, []string{creditID}, true)
	if err != nil || notes[creditID] != "" {
		t.Fatalf("target explicit-empty credit note = (%v, %v)", notes, err)
	}
	if _, err := validateReleaseCreditNotes(values, []string{creditID}, false); errs.Wrap(err).Code() != connect.CodeInvalidArgument {
		t.Fatalf("source empty credit note error = %v", err)
	}
}
