package jobs

import (
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type QueueConfig struct {
	Name        string
	MessageType string
	Workers     int
	Timeout     time.Duration
	RetryLimit  int
}

func DefaultMeshQueueConfig() QueueConfig {
	return QueueConfig{
		Name:        eventpkg.QueueAssetOptimizerMesh,
		MessageType: "api.manage.v1.MeshOptimizationJob",
		Workers:     1,
		Timeout:     20 * time.Minute,
		RetryLimit:  3,
	}
}
