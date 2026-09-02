package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestDecodeWaveformResultUnwrapsTerminalOutcomes(t *testing.T) {
	t.Parallel()

	completed := &managev1.WaveformCompleteEvent{EventId: "waveform-1"}
	failed := &managev1.WaveformFailEvent{EventId: "waveform-2", Error: "decode failed"}

	for _, test := range []struct {
		name      string
		result    *managev1.WaveformResultEvent
		want      proto.Message
		completed bool
	}{
		{
			name:      "completed",
			result:    &managev1.WaveformResultEvent{Outcome: &managev1.WaveformResultEvent_Completed{Completed: completed}},
			want:      completed,
			completed: true,
		},
		{
			name:   "failed",
			result: &managev1.WaveformResultEvent{Outcome: &managev1.WaveformResultEvent_Failed{Failed: failed}},
			want:   failed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, err := proto.Marshal(test.result)
			require.NoError(t, err)
			got, err := DecodeWaveformResult(body)
			require.NoError(t, err)
			require.Equal(t, test.completed, got.Complete)
			requireDecodedProtoEqual(t, test.want, got.Body)
		})
	}
}

func TestDecodeMeshOptimizationResultUnwrapsTerminalOutcomes(t *testing.T) {
	t.Parallel()

	completed := &managev1.MeshOptimizationCompleteEvent{JobId: "mesh-1"}
	failed := &managev1.MeshOptimizationFailEvent{JobId: "mesh-2", Error: "optimize failed"}

	for _, test := range []struct {
		name      string
		result    *managev1.MeshOptimizationResultEvent
		want      proto.Message
		completed bool
	}{
		{
			name:      "completed",
			result:    &managev1.MeshOptimizationResultEvent{Outcome: &managev1.MeshOptimizationResultEvent_Completed{Completed: completed}},
			want:      completed,
			completed: true,
		},
		{
			name:   "failed",
			result: &managev1.MeshOptimizationResultEvent{Outcome: &managev1.MeshOptimizationResultEvent_Failed{Failed: failed}},
			want:   failed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, err := proto.Marshal(test.result)
			require.NoError(t, err)
			got, err := DecodeMeshOptimizationResult(body)
			require.NoError(t, err)
			require.Equal(t, test.completed, got.Complete)
			requireDecodedProtoEqual(t, test.want, got.Body)
		})
	}
}

func TestDecodeMediaResultsRejectMissingOutcome(t *testing.T) {
	t.Parallel()

	waveformBody, err := proto.Marshal(&managev1.WaveformResultEvent{})
	require.NoError(t, err)
	_, err = DecodeWaveformResult(waveformBody)
	require.ErrorContains(t, err, "outcome is required")

	meshBody, err := proto.Marshal(&managev1.MeshOptimizationResultEvent{})
	require.NoError(t, err)
	_, err = DecodeMeshOptimizationResult(meshBody)
	require.ErrorContains(t, err, "outcome is required")
}

func requireDecodedProtoEqual(t *testing.T, want proto.Message, body []byte) {
	t.Helper()
	got := want.ProtoReflect().Type().New().Interface()
	require.NoError(t, proto.Unmarshal(body, got))
	require.True(t, proto.Equal(want, got))
}
