package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names; a new API status fails here until
// it is mapped.
func TestWorkStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.WorkStatus_name {
		status := managev1.WorkStatus(value)
		if status == managev1.WorkStatus_WORK_STATUS_UNSPECIFIED {
			continue
		}
		if got := workStatusToProto(model.WorkStatus(name)); got != status {
			t.Fatalf("%s maps to %s", name, got)
		}
	}
	if got := workStatusToProto(model.WorkStatus("WORK_STATUS_DELETED")); got != managev1.WorkStatus_WORK_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}
