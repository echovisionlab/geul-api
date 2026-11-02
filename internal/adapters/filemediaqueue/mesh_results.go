package filemediaqueue

import (
	"slices"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func MeshProgressEvent(event *managev1.MeshOptimizationProgressEvent) *domain.MeshProgressEvent {
	if event == nil {
		return nil
	}
	return &domain.MeshProgressEvent{JobID: event.JobId, CorrelationID: event.CorrelationId, Identity: meshIdentity(event.Identity), TimestampMS: event.TimestampMs}
}
func MeshCompleteEvent(event *managev1.MeshOptimizationCompleteEvent) *domain.MeshCompleteEvent {
	if event == nil {
		return nil
	}
	result := &domain.MeshCompleteEvent{JobID: event.JobId, CorrelationID: event.CorrelationId, Identity: meshIdentity(event.Identity), ProcessingTimeMS: event.ProcessingTimeMs, TimestampMS: event.TimestampMs}
	if output := event.Output; output != nil {
		result.Output = &domain.MeshOutput{OriginalSizeBytes: cloneMeshValue(output.OriginalSizeBytes), OptimizedSizeBytes: cloneMeshValue(output.OptimizedSizeBytes), OriginalVertexCount: cloneMeshValue(output.OriginalVertexCount), OptimizedVertexCount: cloneMeshValue(output.OptimizedVertexCount), OriginalTriangleCount: cloneMeshValue(output.OriginalTriangleCount), OptimizedTriangleCount: cloneMeshValue(output.OptimizedTriangleCount), Profile: int32(output.Profile)}
		if written := output.Written; written != nil {
			result.Output.Written = &domain.MeshWritten{FileID: written.FileId, FileSize: written.FileSize, SHA256: slices.Clone(written.Sha256)}
		}
	}
	return result
}
func MeshFailedEvent(event *managev1.MeshOptimizationFailEvent) *domain.MeshFailedEvent {
	if event == nil {
		return nil
	}
	return &domain.MeshFailedEvent{JobID: event.JobId, CorrelationID: event.CorrelationId, Identity: meshIdentity(event.Identity), Reason: event.Reason.String(), Error: event.Error, TimestampMS: event.TimestampMs}
}
func meshIdentity(value *managev1.MeshOptimizationIdentity) *domain.MeshIdentity {
	if value == nil {
		return nil
	}
	return &domain.MeshIdentity{EntityType: int32(value.EntityType), EntityID: value.EntityId, FileID: value.FileId}
}
func cloneMeshValue(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return new(*value)
}
