package release

import (
	"testing"

	"connectrpc.com/connect"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

func TestValidateReleaseCreditNotesPreservesTargetExplicitEmpty(t *testing.T) {
	creditID := uuid.NewString()
	values := []*intrav1.ReleaseCreditNote{{CreditId: creditID, Note: ""}}

	notes, err := validateReleaseCreditNotes(values, []string{creditID}, true)
	if err != nil || notes[creditID] != "" {
		t.Fatalf("target explicit-empty credit note = (%v, %v)", notes, err)
	}
	if _, err := validateReleaseCreditNotes(values, []string{creditID}, false); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("source empty credit note error = %v", err)
	}
}
