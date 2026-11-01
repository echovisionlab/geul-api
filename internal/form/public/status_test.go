package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Stored values equal the API enum names, so every public API status except
// UNSPECIFIED must be the mapping of the stored value of the same name; a new
// API status fails here until it is mapped.
func TestFormStatusToOpenCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range openv1.FormStatus_name {
		status := openv1.FormStatus(value)
		if status == openv1.FormStatus_FORM_STATUS_UNSPECIFIED {
			continue
		}
		if got := formStatusToOpen(model.FormStatus(name)); got != status {
			t.Fatalf("stored %q maps to %s", name, got)
		}
	}
	if got := formStatusToOpen(model.FormStatus("FORM_STATUS_CORRUPT")); got != openv1.FormStatus_FORM_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}
