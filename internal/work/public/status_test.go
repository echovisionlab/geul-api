package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Stored values equal the API enum names; a new public status fails here
// until it is mapped.
func TestWorkStatusMappingCoversEveryPublicStatus(t *testing.T) {
	t.Parallel()
	for value, name := range openv1.WorkStatus_name {
		status := openv1.WorkStatus(value)
		if status == openv1.WorkStatus_WORK_STATUS_UNSPECIFIED {
			continue
		}
		if got := workStatusToProto(model.WorkStatus(name)); got != status {
			t.Fatalf("%s maps to %s", name, got)
		}
	}
	if got := workStatusToProto(model.WorkStatus("WORK_STATUS_DELETED")); got != openv1.WorkStatus_WORK_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}
