package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Stored values equal the API enum names; a new API status fails here until
// it is mapped.
func TestPostStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range openv1.PostStatus_name {
		status := openv1.PostStatus(value)
		if status == openv1.PostStatus_POST_STATUS_UNSPECIFIED {
			continue
		}
		if got := postStatusToProto(model.PostStatus(name)); got != status {
			t.Fatalf("%s maps to %s", name, got)
		}
	}
	if got := postStatusToProto(model.PostStatus("POST_STATUS_DELETED")); got != openv1.PostStatus_POST_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}
