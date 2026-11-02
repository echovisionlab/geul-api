package post

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names; a new API status fails here until
// it is mapped.
func TestPostStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.PostStatus_name {
		status := managev1.PostStatus(value)
		if status == managev1.PostStatus_POST_STATUS_UNSPECIFIED {
			continue
		}
		if got := postStatusToProto(Status(name)); got != status {
			t.Fatalf("%s maps to %s", name, got)
		}
	}
	if got := postStatusToProto(Status("POST_STATUS_DELETED")); got != managev1.PostStatus_POST_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}
