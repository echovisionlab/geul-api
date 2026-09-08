package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apptelemetry "github.com/echovisionlab/geul-api/internal/assetprocessing/telemetry"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/assetprocessing/optimizer"
)

const defaultMaxInputBytes int64 = 50 * 1024 * 1024

var meshJobTracer = otel.Tracer(sharedtelemetry.ServiceAssetOptimizer.Instrumentation("mesh_optimization"))

type WorkDirManager interface {
	CreateJobWorkDir(jobID string) (string, error)
	CleanupJobWorkDir(path string) error
}

type StorageClient interface {
	GetObjectSize(ctx context.Context, key string) (int64, error)
	Download(ctx context.Context, key string, localPath string) error
	Upload(ctx context.Context, key string, localPath string, contentType string) error
}

type MeshOptimizer interface {
	Inspect(ctx context.Context, path string) (optimizer.Inspection, error)
	Optimize(ctx context.Context, inputPath, outputPath string, options optimizer.Options) error
}

type EventPublisher interface {
	PublishMeshOptimizationProgress(ctx context.Context, event *apiv1.MeshOptimizationProgressEvent) error
	PublishMeshOptimizationComplete(ctx context.Context, event *apiv1.MeshOptimizationCompleteEvent) error
	PublishMeshOptimizationFail(ctx context.Context, event *apiv1.MeshOptimizationFailEvent) error
}

// ResultPublishError keeps the original command retryable until its stable
// terminal result has been durably enqueued in PGMQ.
type ResultPublishError struct{ cause error }

func (e *ResultPublishError) Error() string   { return e.cause.Error() }
func (e *ResultPublishError) Unwrap() error   { return e.cause }
func (e *ResultPublishError) Retryable() bool { return true }

// TerminalResultError acknowledges a command whose failure was durably
// published even though processing itself failed.
type TerminalResultError struct{ cause error }

func (e *TerminalResultError) Error() string        { return e.cause.Error() }
func (e *TerminalResultError) Unwrap() error        { return e.cause }
func (e *TerminalResultError) TerminalResult() bool { return true }

type Config struct {
	MaxInputBytes int64
}

type Processor struct {
	config    Config
	workDirs  WorkDirManager
	storage   StorageClient
	optimizer MeshOptimizer
	publisher EventPublisher
}

func NewProcessor(config Config, workDirs WorkDirManager, storage StorageClient, meshOptimizer MeshOptimizer, publisher EventPublisher) (*Processor, error) {
	if config.MaxInputBytes <= 0 {
		config.MaxInputBytes = defaultMaxInputBytes
	}
	if workDirs == nil {
		return nil, fmt.Errorf("work dir manager is required")
	}
	if storage == nil {
		return nil, fmt.Errorf("storage client is required")
	}
	if meshOptimizer == nil {
		return nil, fmt.Errorf("optimizer is required")
	}
	if publisher == nil {
		return nil, fmt.Errorf("publisher is required")
	}

	return &Processor{
		config:    config,
		workDirs:  workDirs,
		storage:   storage,
		optimizer: meshOptimizer,
		publisher: publisher,
	}, nil
}

func (p *Processor) HandleMeshJob(ctx context.Context, body []byte) error {
	var job apiv1.MeshOptimizationJob
	if err := proto.Unmarshal(body, &job); err != nil {
		return fmt.Errorf("parse mesh optimization job: %w", err)
	}
	if err := validateJobID(&job); err != nil {
		return err
	}
	ctx, span := meshJobTracer.Start(ctx, "mesh.optimization.process",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("job.id", job.GetJobId())),
	)
	defer span.End()
	err := p.processJob(ctx, &job)
	if err != nil {
		span.SetAttributes(attribute.String("error.type", sharedtelemetry.StableErrorType(err)))
		span.SetStatus(codes.Error, "")
	}
	return err
}

func (p *Processor) processJob(ctx context.Context, job *apiv1.MeshOptimizationJob) error {
	startedAt := time.Now()
	jobID := strings.TrimSpace(job.GetJobId())

	startedRecord, startedRecordErr := sharedtelemetry.NewJobStartedRecord(
		apptelemetry.SystemMetadata(ctx, startedAt),
		sharedtelemetry.JobContext{JobKind: sharedtelemetry.JobKindMeshOptimization, JobID: jobID},
	)
	_ = apptelemetry.EmitSystem(ctx, startedRecord, startedRecordErr)

	spec, err := newMeshJobSpec(job)
	if err != nil {
		return p.publishFail(ctx, job, startedAt, apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED, err)
	}

	result, err := p.runPipeline(ctx, job, spec)
	if err != nil {
		return p.publishFail(ctx, job, startedAt, pipelineFailureReason(err), err)
	}

	complete := newCompleteEvent(job, spec, result, startedAt)
	if err := p.publisher.PublishMeshOptimizationComplete(ctx, complete); err != nil {
		return &ResultPublishError{cause: fmt.Errorf("publish complete result: %w", err)}
	}
	p.publishProgress(ctx, job, optimizationCompleted)

	succeededRecord, succeededRecordErr := sharedtelemetry.NewJobSucceededRecord(
		apptelemetry.SystemMetadata(ctx, time.Now()),
		sharedtelemetry.JobContext{JobKind: sharedtelemetry.JobKindMeshOptimization, JobID: jobID},
		time.Since(startedAt).Milliseconds(),
	)
	_ = apptelemetry.EmitSystem(ctx, succeededRecord, succeededRecordErr)
	return nil
}

func pipelineFailureReason(err error) apiv1.MeshOptimizationFailureReason {
	var failure *pipelineFailure
	if errors.As(err, &failure) {
		return failure.reason
	}
	return apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_INTERNAL
}
