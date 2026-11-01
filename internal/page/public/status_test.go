package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/page"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Every public API status except UNSPECIFIED is a stored Page status of the
// same name; a new public status fails here until it is mapped.
func TestPublicPageStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range openv1.PageStatus_name {
		status := openv1.PageStatus(value)
		if status == openv1.PageStatus_PAGE_STATUS_UNSPECIFIED {
			continue
		}
		if got := toProtoStatus(page.Status(name)); got != status {
			t.Fatalf("stored %s maps to %s", name, got)
		}
	}
	if got := toProtoStatus(page.Status("PAGE_STATUS_ARCHIVED")); got != openv1.PageStatus_PAGE_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}
