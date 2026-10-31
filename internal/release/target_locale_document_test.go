package release

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

func TestReleaseSnapshotContainsLocaleTreatsEmptyOverlayAsPresent(t *testing.T) {
	if !releaseSnapshotContainsLocale(contentblock.Snapshot{
		LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "ko"}},
	}, "ko") {
		t.Fatal("empty target overlay was treated as missing")
	}
	if releaseSnapshotContainsLocale(contentblock.Snapshot{
		LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "ko"}},
	}, "fr") {
		t.Fatal("different target locale was treated as present")
	}
}

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
