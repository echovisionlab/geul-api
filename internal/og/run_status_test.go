package og

import "testing"

func TestSettledRunStatusFollowsItsGenerations(t *testing.T) {
	for name, test := range map[string]struct {
		counts    map[GenerationStatus]int64
		want      RunStatus
		completed bool
	}{
		"no generations":           {counts: map[GenerationStatus]int64{}, want: RunRunning},
		"one still active":         {counts: map[GenerationStatus]int64{StatusReady: 1, StatusQueued: 1}, want: RunRunning},
		"every one ready":          {counts: map[GenerationStatus]int64{StatusReady: 2}, want: RunReady, completed: true},
		"cancelled and superseded": {counts: map[GenerationStatus]int64{StatusCancelled: 1, StatusSuperseded: 1}, want: RunCancelled, completed: true},
		"ready beside failed":      {counts: map[GenerationStatus]int64{StatusReady: 1, StatusFailed: 1}, want: RunPartialFailed, completed: true},
		"ready beside cancelled":   {counts: map[GenerationStatus]int64{StatusReady: 1, StatusCancelled: 1}, want: RunCancelled, completed: true},
		"failed beside superseded": {counts: map[GenerationStatus]int64{StatusFailed: 1, StatusSuperseded: 1}, want: RunFailed, completed: true},
	} {
		t.Run(name, func(t *testing.T) {
			status, completed := settledRunStatus(test.counts)
			if status != test.want || completed != test.completed {
				t.Fatalf("settledRunStatus() = %s, %t; want %s, %t", status, completed, test.want, test.completed)
			}
		})
	}
}
