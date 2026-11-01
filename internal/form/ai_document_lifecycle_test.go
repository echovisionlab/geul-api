package form

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestKnownFormStatusAcceptsOnlyTheStoredStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status model.FormStatus
		known  bool
	}{
		{status: model.FormStatusDraft, known: true},
		{status: model.FormStatusPublished, known: true},
		{status: "PUBLISHED"},
		{status: "FORM_STATUS_PUBLISHED "},
		{status: "BOGUS_PUBLISHED"},
		{status: "FORM_STATUS_ARCHIVED"},
		{status: "FORM_STATUS_UNSPECIFIED"},
	}
	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			t.Parallel()
			if known := knownFormStatus(test.status); known != test.known {
				t.Fatalf("knownFormStatus(%q) = %t, want %t", test.status, known, test.known)
			}
		})
	}
}
