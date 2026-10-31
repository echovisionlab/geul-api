package page

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API status except
// UNSPECIFIED must round-trip; a new API status fails here until it is mapped.
func TestPageStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.PageStatus_name {
		status := managev1.PageStatus(value)
		stored, known := pageStatusFromProto(status)
		if status == managev1.PageStatus_PAGE_STATUS_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name || pageStatusToProto(stored) != status {
			t.Fatalf("%s maps to %q (known %t) and back to %s", name, stored, known, pageStatusToProto(stored))
		}
	}
	if got := pageStatusToProto(model.PageStatus("PAGE_STATUS_ARCHIVED")); got != managev1.PageStatus_PAGE_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}
