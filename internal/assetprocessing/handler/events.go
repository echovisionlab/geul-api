package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	apptelemetry "github.com/echovisionlab/geul-api/internal/assetprocessing/telemetry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type meshProgress struct {
	sequence int64
	percent  int32
	stage    apiv1.MeshOptimizationStage
}

var (
	downloadStarted = meshProgress{
		sequence: 1, percent: 5,
		stage: apiv1.MeshOptimizationStage_MESH_OPTIMIZATION_STAGE_DOWNLOADING,
	}
	analysisStarted = meshProgress{
		sequence: 2, percent: 25,
		stage: apiv1.MeshOptimizationStage_MESH_OPTIMIZATION_STAGE_ANALYZING,
	}
	optimizationStarted = meshProgress{
		sequence: 3, percent: 60,
		stage: apiv1.MeshOptimizationStage_MESH_OPTIMIZATION_STAGE_OPTIMIZING,
	}
	outputAnalysisStarted = meshProgress{
		sequence: 4, percent: 80,
		stage: apiv1.MeshOptimizationStage_MESH_OPTIMIZATION_STAGE_ANALYZING,
	}
	uploadStarted = meshProgress{
		sequence: 5, percent: 90,
		stage: apiv1.MeshOptimizationStage_MESH_OPTIMIZATION_STAGE_UPLOADING,
	}
	optimizationCompleted = meshProgress{
		sequence: 6, percent: 100,
		stage: apiv1.MeshOptimizationStage_MESH_OPTIMIZATION_STAGE_UPLOADING,
	}
)

func newCompleteEvent(
	job *apiv1.MeshOptimizationJob,
	spec meshJobSpec,
	result *pipelineResult,
	startedAt time.Time,
) *apiv1.MeshOptimizationCompleteEvent {
	originalVertexCount := result.before.UploadVertexCount
	optimizedVertexCount := result.after.UploadVertexCount
	originalTriangleCount := result.before.GLPrimitiveCount
	optimizedTriangleCount := result.after.GLPrimitiveCount
	return &apiv1.MeshOptimizationCompleteEvent{
		JobId:         spec.jobID,
		CorrelationId: job.GetCorrelationId(),
		Identity:      cloneIdentity(job.GetIdentity()),
		Output: &apiv1.MeshOptimizationOutput{
			Written: &commonv1.MediaObjectWriteResult{
				FileId:   spec.outputTarget.GetFileId(),
				FileSize: result.outputSize,
				Sha256:   result.outputSHA256,
			},
			OriginalSizeBytes:      &result.inputSize,
			OptimizedSizeBytes:     &result.outputSize,
			CompressionMethod:      apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO,
			TargetRatioPercent:     spec.options.TargetRatioPercent,
			Profile:                spec.options.Profile.EventValue(),
			OriginalVertexCount:    &originalVertexCount,
			OptimizedVertexCount:   &optimizedVertexCount,
			OriginalTriangleCount:  &originalTriangleCount,
			OptimizedTriangleCount: &optimizedTriangleCount,
		},
		ProcessingTimeMs: time.Since(startedAt).Milliseconds(),
		TimestampMs:      time.Now().UnixMilli(),
	}
}

func (p *Processor) publishProgress(ctx context.Context, job *apiv1.MeshOptimizationJob, progress meshProgress) {
	event := &apiv1.MeshOptimizationProgressEvent{
		JobId:          job.GetJobId(),
		CorrelationId:  job.GetCorrelationId(),
		Identity:       cloneIdentity(job.GetIdentity()),
		SequenceNumber: progress.sequence,
		Progress:       progress.percent,
		Stage:          &progress.stage,
		TimestampMs:    time.Now().UnixMilli(),
	}
	if err := p.publisher.PublishMeshOptimizationProgress(ctx, event); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		slog.Warn("Failed to publish mesh optimization progress", "jobId", job.GetJobId(), "progress", progress.percent, "error", err)
	}
}

func (p *Processor) publishFail(ctx context.Context, job *apiv1.MeshOptimizationJob, startedAt time.Time, reason apiv1.MeshOptimizationFailureReason, cause error) error {
	event := &apiv1.MeshOptimizationFailEvent{
		JobId:            job.GetJobId(),
		CorrelationId:    job.GetCorrelationId(),
		Identity:         cloneIdentity(job.GetIdentity()),
		Reason:           reason,
		Error:            cause.Error(),
		ProcessingTimeMs: time.Since(startedAt).Milliseconds(),
		TimestampMs:      time.Now().UnixMilli(),
	}
	if err := p.publisher.PublishMeshOptimizationFail(ctx, event); err != nil {
		return &ResultPublishError{cause: fmt.Errorf("%w; additionally failed to publish mesh optimization failure result: %v", cause, err)}
	}
	failedRecord, failedRecordErr := sharedtelemetry.NewJobFailedRecord(
		apptelemetry.SystemMetadata(ctx, time.Now()),
		sharedtelemetry.JobContext{JobKind: sharedtelemetry.JobKindMeshOptimization, JobID: job.GetJobId()},
		time.Since(startedAt).Milliseconds(),
		sharedtelemetry.JobFailure{Reason: telemetryJobFailureReason(reason)},
	)
	_ = apptelemetry.EmitSystem(ctx, failedRecord, failedRecordErr)
	return &TerminalResultError{cause: cause}
}

func telemetryJobFailureReason(reason apiv1.MeshOptimizationFailureReason) sharedtelemetry.JobFailureReason {
	switch reason {
	case apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED:
		return sharedtelemetry.JobFailureRejected
	case apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_SOURCE_NOT_FOUND:
		return sharedtelemetry.JobFailureSourceNotFound
	case apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_DOWNLOAD_FAILED:
		return sharedtelemetry.JobFailureDownloadFailed
	case apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED:
		return sharedtelemetry.JobFailureOptimizationFailed
	case apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_UPLOAD_FAILED:
		return sharedtelemetry.JobFailureUploadFailed
	case apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_INTERNAL:
		return sharedtelemetry.JobFailureInternal
	default:
		return sharedtelemetry.JobFailureInternal
	}
}
