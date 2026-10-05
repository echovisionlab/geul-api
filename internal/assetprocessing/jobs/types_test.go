package jobs

import (
	"testing"
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestDefaultMeshQueueConfig(t *testing.T) {
	got := DefaultMeshQueueConfig()
	if got.Name != eventpkg.QueueAssetOptimizerMesh ||
		got.RetryLimit != 3 || got.Workers != 1 ||
		got.Timeout != 20*time.Minute {
		t.Fatalf("unexpected queue config: %#v", got)
	}
}
