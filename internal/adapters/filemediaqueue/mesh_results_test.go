package filemediaqueue

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestMeshResultProjectionPreservesIdentityOutputAndOptionalStatistics(t *testing.T) {
	t.Parallel()
	identity := &managev1.MeshOptimizationIdentity{EntityType: 999, EntityId: "entity", FileId: "source"}
	progress := MeshProgressEvent(&managev1.MeshOptimizationProgressEvent{JobId: "job", CorrelationId: "candidate", Identity: identity, TimestampMs: 123})
	require.Equal(t, &domain.MeshProgressEvent{JobID: "job", CorrelationID: "candidate", Identity: &domain.MeshIdentity{EntityType: 999, EntityID: "entity", FileID: "source"}, TimestampMS: 123}, progress)
	event := &managev1.MeshOptimizationCompleteEvent{JobId: "job", CorrelationId: "candidate", Identity: identity, ProcessingTimeMs: 45, TimestampMs: 123, Output: &managev1.MeshOptimizationOutput{Written: &commonv1.MediaObjectWriteResult{FileId: "output", FileSize: 512, Sha256: []byte{1, 2, 3}}, OriginalSizeBytes: new(int64(1024)), OptimizedSizeBytes: new(int64(512)), OriginalVertexCount: new(int64(100)), OptimizedVertexCount: new(int64(50)), OriginalTriangleCount: new(int64(0)), Profile: 998}}
	result := MeshCompleteEvent(event)
	require.Equal(t, &domain.MeshCompleteEvent{JobID: "job", CorrelationID: "candidate", Identity: &domain.MeshIdentity{EntityType: 999, EntityID: "entity", FileID: "source"}, ProcessingTimeMS: 45, TimestampMS: 123, Output: &domain.MeshOutput{Written: &domain.MeshWritten{FileID: "output", FileSize: 512, SHA256: []byte{1, 2, 3}}, OriginalSizeBytes: new(int64(1024)), OptimizedSizeBytes: new(int64(512)), OriginalVertexCount: new(int64(100)), OptimizedVertexCount: new(int64(50)), OriginalTriangleCount: new(int64(0)), Profile: 998}}, result)
	event.Identity.FileId = "changed"
	event.Output.Written.Sha256[0] = 9
	*event.Output.OriginalSizeBytes = 0
	*event.Output.OptimizedSizeBytes = 0
	*event.Output.OriginalVertexCount = 0
	*event.Output.OptimizedVertexCount = 0
	*event.Output.OriginalTriangleCount = 9
	require.Equal(t, "source", result.Identity.FileID)
	require.Equal(t, []byte{1, 2, 3}, result.Output.Written.SHA256)
	require.EqualValues(t, 1024, *result.Output.OriginalSizeBytes)
	require.EqualValues(t, 512, *result.Output.OptimizedSizeBytes)
	require.EqualValues(t, 100, *result.Output.OriginalVertexCount)
	require.EqualValues(t, 50, *result.Output.OptimizedVertexCount)
	require.EqualValues(t, 0, *result.Output.OriginalTriangleCount)
	require.Nil(t, result.Output.OptimizedTriangleCount)
}

func TestMeshResultProjectionPreservesMissingPayloadsAndReasonLabels(t *testing.T) {
	t.Parallel()
	require.Nil(t, MeshProgressEvent(nil))
	require.Nil(t, MeshCompleteEvent(nil))
	require.Nil(t, MeshFailedEvent(nil))
	require.Nil(t, MeshProgressEvent(&managev1.MeshOptimizationProgressEvent{}).Identity)
	empty := MeshCompleteEvent(&managev1.MeshOptimizationCompleteEvent{})
	require.Nil(t, empty.Identity)
	require.Nil(t, empty.Output)
	empty = MeshCompleteEvent(&managev1.MeshOptimizationCompleteEvent{Output: &managev1.MeshOptimizationOutput{}})
	require.Nil(t, empty.Output.Written)
	require.Nil(t, empty.Output.OptimizedSizeBytes)
	for _, test := range []struct {
		reason managev1.MeshOptimizationFailureReason
		label  string
	}{
		{0, domain.MeshFailureUnspecified},
		{managev1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED, domain.MeshFailureRejected},
		{999, "999"},
	} {
		result := MeshFailedEvent(&managev1.MeshOptimizationFailEvent{JobId: "job", CorrelationId: "candidate", Reason: test.reason, Error: " failure ", TimestampMs: 123})
		require.Equal(t, &domain.MeshFailedEvent{JobID: "job", CorrelationID: "candidate", Reason: test.label, Error: " failure ", TimestampMS: 123}, result)
	}
}
