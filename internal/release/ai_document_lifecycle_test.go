package release

import (
	"testing"
)

func TestReleaseAIDocumentLifecycleUsesExactStoredStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status Status
		valid  bool
	}{
		{status: StatusDraft, valid: true},
		{status: StatusPublished, valid: true},
		{status: "PUBLISHED"},
		{status: "RELEASE_STATUS_PUBLISHED "},
		{status: "BOGUS_PUBLISHED"},
		{status: "RELEASE_STATUS_ARCHIVED"},
		{status: "RELEASE_STATUS_UNSPECIFIED"},
	}
	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			t.Parallel()
			if valid := knownReleaseLifecycle(test.status); valid != test.valid {
				t.Fatalf("knownReleaseLifecycle(%q) = %t, want %t", test.status, valid, test.valid)
			}
		})
	}
}
