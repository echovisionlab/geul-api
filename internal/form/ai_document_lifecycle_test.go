package form

import (
	"testing"
)

func TestStatusKnownAcceptsOnlyTheStoredStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status Status
		known  bool
	}{
		{status: StatusDraft, known: true},
		{status: StatusPublished, known: true},
		{status: "PUBLISHED"},
		{status: "FORM_STATUS_PUBLISHED "},
		{status: "BOGUS_PUBLISHED"},
		{status: "FORM_STATUS_ARCHIVED"},
		{status: "FORM_STATUS_UNSPECIFIED"},
	}
	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			t.Parallel()
			if known := test.status.Known(); known != test.known {
				t.Fatalf("Status(%q).Known() = %t, want %t", test.status, known, test.known)
			}
		})
	}
}
