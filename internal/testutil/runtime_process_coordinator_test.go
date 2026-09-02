//go:build integration

package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeApplicationCoordinatorStopsAndRestartsVariants(t *testing.T) {
	coordinator := &runtimeApplicationCoordinator{}
	healthy := &RuntimeStack{}
	failing := &RuntimeStack{}
	direct := &RuntimeStack{}
	healthyStarts := 0
	failingStarts := 0

	startHealthy := func() {
		healthyStarts++
		healthy.backendProc = stoppedRuntimeProcess("healthy-backend")
		healthy.collabProc = stoppedRuntimeProcess("healthy-collab")
	}
	startFailing := func() {
		failingStarts++
		failing.waveformProc = stoppedRuntimeProcess("failing-waveform")
	}

	require.NoError(t, coordinator.activate(t.Context(), healthy, startHealthy))
	require.NoError(t, coordinator.activate(t.Context(), failing, startFailing))
	require.Nil(t, healthy.backendProc)
	require.Nil(t, healthy.collabProc)
	require.Equal(t, 1, healthyStarts)
	require.Equal(t, 1, failingStarts)

	require.NoError(t, coordinator.activate(t.Context(), direct, nil))
	require.Nil(t, failing.waveformProc)
	require.NoError(t, coordinator.activate(t.Context(), healthy, startHealthy))
	require.Equal(t, 2, healthyStarts, "revisited variants must start fresh processes")
	require.NotNil(t, healthy.backendProc)
}

func stoppedRuntimeProcess(name string) *runtimeProcess {
	done := make(chan struct{})
	close(done)
	return &runtimeProcess{
		name:   name,
		pid:    99999999,
		done:   done,
		cancel: func() {},
	}
}

func TestRuntimeProcessStopIsIdempotent(t *testing.T) {
	process := stoppedRuntimeProcess("idempotent")
	require.NoError(t, process.stop(context.Background()))
	require.NoError(t, process.stop(context.Background()))
}
