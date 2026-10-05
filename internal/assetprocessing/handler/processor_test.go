package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"testing"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/echovisionlab/geul-api/internal/assetprocessing/optimizer"
)

func TestHandleMeshJobPublishesCompleteAndCleansWorkDir(t *testing.T) {
	tempRoot := t.TempDir()
	processor, storage, meshOptimizer, publisher := newTestProcessor(t, tempRoot)
	job := validJob()

	err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, job))
	if err != nil {
		t.Fatalf("handle mesh job: %v", err)
	}

	assertTempRootEmpty(t, tempRoot)
	if len(meshOptimizer.inspectCalls) != 2 {
		t.Fatalf("expected source and output inspection, got %d", len(meshOptimizer.inspectCalls))
	}
	if len(storage.uploads) != 1 || storage.uploads[testOutputKey].contentType != "model/gltf-binary" {
		t.Fatalf("expected optimized GLB upload, got %#v", storage.uploads)
	}
	if len(publisher.completeEvents) != 1 {
		t.Fatalf("expected complete event, got %d", len(publisher.completeEvents))
	}
	complete := publisher.completeEvents[0]
	output := complete.GetOutput()
	if output.GetWritten().GetFileId() != testOutputFileID {
		t.Fatalf("unexpected output file id: %q", output.GetWritten().GetFileId())
	}
	if output.GetWritten().GetFileSize() != int64(len("optimized-glb")) {
		t.Fatalf("unexpected optimized size: %d", output.GetWritten().GetFileSize())
	}
	wantSHA := sha256.Sum256([]byte("optimized-glb"))
	if string(output.GetWritten().GetSha256()) != string(wantSHA[:]) {
		t.Fatalf("unexpected optimized SHA-256")
	}
	if output.GetCompressionMethod() != apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO {
		t.Fatalf("unexpected compression method: %v", output.GetCompressionMethod())
	}
	if output.GetProfile() != apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1 {
		t.Fatalf("unexpected profile: %v", output.GetProfile())
	}
	if len(meshOptimizer.optimizeOptions) != 1 || meshOptimizer.optimizeOptions[0].Profile != optimizer.ProfileParticleMeshV1 {
		t.Fatalf("unexpected optimizer options: %#v", meshOptimizer.optimizeOptions)
	}
	if output.GetOriginalVertexCount() != 300 || output.GetOptimizedVertexCount() != 150 {
		t.Fatalf("unexpected vertex counts: original=%d optimized=%d", output.GetOriginalVertexCount(), output.GetOptimizedVertexCount())
	}
	if output.GetOriginalTriangleCount() != 120 || output.GetOptimizedTriangleCount() != 45 {
		t.Fatalf("unexpected triangle counts: original=%d optimized=%d", output.GetOriginalTriangleCount(), output.GetOptimizedTriangleCount())
	}
	if len(publisher.failEvents) != 0 {
		t.Fatalf("unexpected fail events: %#v", publisher.failEvents)
	}
	if len(publisher.progressEvents) == 0 {
		t.Fatalf("expected progress events")
	}
}

func TestHandleMeshJobCreatesInternalJobSpan(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})

	processor, _, _, _ := newTestProcessor(t, t.TempDir())
	job := validJob()
	if err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, job)); err != nil {
		t.Fatalf("HandleMeshJob() error = %v", err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "mesh.optimization.process" || spans[0].SpanKind() != trace.SpanKindInternal {
		t.Fatalf("job spans = %#v", spans)
	}
}

func TestHandleMeshJobRejectsUnspecifiedProfile(t *testing.T) {
	processor, _, meshOptimizer, publisher := newTestProcessor(t, t.TempDir())
	job := validJob()
	job.Options.Profile = apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED

	if err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, job)); err == nil {
		t.Fatal("expected unspecified profile rejection")
	}
	if len(meshOptimizer.optimizeOptions) != 0 {
		t.Fatalf("optimizer calls: %#v", meshOptimizer.optimizeOptions)
	}
	if len(publisher.completeEvents) != 0 {
		t.Fatalf("complete events: %#v", publisher.completeEvents)
	}
	if len(publisher.failEvents) != 1 || publisher.failEvents[0].GetReason() != apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED {
		t.Fatalf("failure events: %#v", publisher.failEvents)
	}
}

func TestHandleMeshJobRejectsOversizedInput(t *testing.T) {
	tempRoot := t.TempDir()
	processor, storage, _, publisher := newTestProcessor(t, tempRoot)
	storage.size = 51
	processor.config.MaxInputBytes = 50

	err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, validJob()))
	if err == nil {
		t.Fatalf("expected rejection error")
	}

	assertTempRootEmpty(t, tempRoot)
	if storage.downloaded {
		t.Fatalf("oversized source should not be downloaded")
	}
	if len(publisher.failEvents) != 1 {
		t.Fatalf("expected one fail event, got %d", len(publisher.failEvents))
	}
	if publisher.failEvents[0].GetReason() != apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED {
		t.Fatalf("unexpected fail reason: %v", publisher.failEvents[0].GetReason())
	}
}

func TestHandleMeshJobCleansWorkDirOnOptimizerFailure(t *testing.T) {
	tempRoot := t.TempDir()
	processor, storage, optimizer, publisher := newTestProcessor(t, tempRoot)
	optimizer.optimizeErr = errors.New("optimizer failed")

	err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, validJob()))
	if err == nil {
		t.Fatalf("expected optimizer error")
	}

	assertTempRootEmpty(t, tempRoot)
	if len(storage.uploads) != 0 {
		t.Fatalf("optimizer failure should not upload output")
	}
	if len(publisher.failEvents) != 1 {
		t.Fatalf("expected one fail event, got %d", len(publisher.failEvents))
	}
	if publisher.failEvents[0].GetReason() != apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED {
		t.Fatalf("unexpected fail reason: %v", publisher.failEvents[0].GetReason())
	}
}

func TestHandleMeshJobPreservesOutputWhenCompletePublishIsUncertain(t *testing.T) {
	tempRoot := t.TempDir()
	processor, storage, _, publisher := newTestProcessor(t, tempRoot)
	publisher.completeErr = errors.New("publish complete failed")
	job := validJob()

	err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, job))
	if err == nil {
		t.Fatalf("expected publish error")
	}

	assertTempRootEmpty(t, tempRoot)
	if storage.deleted[testOutputKey] {
		t.Fatalf("uploaded output must be preserved for command redelivery")
	}
	var retryable *ResultPublishError
	if !errors.As(err, &retryable) {
		t.Fatalf("expected retryable result publish error, got %T", err)
	}
	if len(publisher.failEvents) != 0 {
		t.Fatalf("uncertain success must not be converted into failure, got %d fail events", len(publisher.failEvents))
	}
}

func TestResultErrorContracts(t *testing.T) {
	cause := errors.New("broker unavailable")
	retryable := &ResultPublishError{cause: cause}
	if retryable.Error() != cause.Error() || !errors.Is(retryable, cause) || !retryable.Retryable() {
		t.Fatalf("unexpected retryable result error contract: %#v", retryable)
	}
	terminal := &TerminalResultError{cause: cause}
	if terminal.Error() != cause.Error() || !errors.Is(terminal, cause) || !terminal.TerminalResult() {
		t.Fatalf("unexpected terminal result error contract: %#v", terminal)
	}
	pipelineErr := pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_DOWNLOAD_FAILED, "download", cause)
	if !errors.Is(pipelineErr, cause) || pipelineFailureReason(pipelineErr) != apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_DOWNLOAD_FAILED {
		t.Fatalf("unexpected pipeline error contract: %#v", pipelineErr)
	}
	if pipelineFailureReason(cause) != apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_INTERNAL {
		t.Fatal("untyped pipeline errors must use the internal reason")
	}
}

func TestTelemetryJobFailureReasonCatalogMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input apiv1.MeshOptimizationFailureReason
		want  sharedtelemetry.JobFailureReason
	}{
		{apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED, sharedtelemetry.JobFailureRejected},
		{apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_SOURCE_NOT_FOUND, sharedtelemetry.JobFailureSourceNotFound},
		{apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_DOWNLOAD_FAILED, sharedtelemetry.JobFailureDownloadFailed},
		{apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED, sharedtelemetry.JobFailureOptimizationFailed},
		{apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_UPLOAD_FAILED, sharedtelemetry.JobFailureUploadFailed},
		{apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_INTERNAL, sharedtelemetry.JobFailureInternal},
		{apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_UNSPECIFIED, sharedtelemetry.JobFailureInternal},
	}
	for _, test := range tests {
		if got := telemetryJobFailureReason(test.input); got != test.want {
			t.Fatalf("telemetryJobFailureReason(%s) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestProcessJobPublishesValidationFailure(t *testing.T) {
	processor, _, _, publisher := newTestProcessor(t, t.TempDir())
	job := validJob()
	job.CorrelationId = ""
	if err := processor.processJob(context.Background(), job); err == nil {
		t.Fatal("expected validation failure")
	}
	if len(publisher.failEvents) != 1 || publisher.failEvents[0].GetReason() != apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED {
		t.Fatalf("expected rejected terminal event, got %#v", publisher.failEvents)
	}
}

func TestNewProcessorDependencies(t *testing.T) {
	workDirs := &fakeWorkDirs{root: t.TempDir()}
	storage := &fakeStorage{}
	meshOptimizer := &fakeOptimizer{}
	publisher := &fakePublisher{}
	processor, err := NewProcessor(Config{}, workDirs, storage, meshOptimizer, publisher)
	if err != nil {
		t.Fatal(err)
	}
	if processor.config.MaxInputBytes != defaultMaxInputBytes {
		t.Fatalf("expected default max input bytes, got %d", processor.config.MaxInputBytes)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{name: "work dirs", call: func() error { _, err := NewProcessor(Config{}, nil, storage, meshOptimizer, publisher); return err }},
		{name: "storage", call: func() error { _, err := NewProcessor(Config{}, workDirs, nil, meshOptimizer, publisher); return err }},
		{name: "optimizer", call: func() error { _, err := NewProcessor(Config{}, workDirs, storage, nil, publisher); return err }},
		{name: "publisher", call: func() error { _, err := NewProcessor(Config{}, workDirs, storage, meshOptimizer, nil); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("expected dependency error")
			}
		})
	}
}

func TestHandleMeshJobRejectsMalformedPayload(t *testing.T) {
	processor, _, _, _ := newTestProcessor(t, t.TempDir())
	if err := processor.HandleMeshJob(context.Background(), []byte{0xff}); err == nil {
		t.Fatal("expected protobuf parse error")
	}
}

func TestHandleMeshJobRejectsInvalidJobIDWithoutPublishingResult(t *testing.T) {
	processor, _, _, publisher := newTestProcessor(t, t.TempDir())
	job := validJob()
	job.JobId = "not-a-uuid"
	if err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, job)); err == nil {
		t.Fatal("expected invalid job id error")
	}
	if len(publisher.failEvents) != 0 || len(publisher.completeEvents) != 0 {
		t.Fatalf("invalid job identity must be dead-lettered without a result: failures=%d completions=%d", len(publisher.failEvents), len(publisher.completeEvents))
	}
}

func TestProcessJobFailureBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		job   func() *apiv1.MeshOptimizationJob
		setup func(*Processor, *fakeStorage, *fakeOptimizer, *fakePublisher)
	}{
		{
			name: "validation",
			job: func() *apiv1.MeshOptimizationJob {
				job := validJob()
				job.JobId = ""
				return job
			},
		},
		{
			name: "options",
			job: func() *apiv1.MeshOptimizationJob {
				job := validJob()
				job.Options = nil
				return job
			},
		},
		{name: "work dir", job: validJob, setup: func(processor *Processor, _ *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			processor.workDirs = &fakeWorkDirs{createErr: errors.New("create failed")}
		}},
		{name: "source size", job: validJob, setup: func(_ *Processor, storage *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			storage.sizeErr = errors.New("head failed")
		}},
		{name: "download", job: validJob, setup: func(_ *Processor, storage *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			storage.downloadErr = errors.New("download failed")
		}},
		{name: "download stat", job: validJob, setup: func(_ *Processor, _ *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			statFile = func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") }
		}},
		{name: "downloaded size", job: validJob, setup: func(processor *Processor, storage *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			processor.config.MaxInputBytes = 3
			storage.payload = []byte("oversized")
		}},
		{name: "source inspection", job: validJob, setup: func(_ *Processor, _ *fakeStorage, meshOptimizer *fakeOptimizer, _ *fakePublisher) {
			meshOptimizer.inspectErrAt = 1
		}},
		{name: "optimized inspection", job: validJob, setup: func(_ *Processor, _ *fakeStorage, meshOptimizer *fakeOptimizer, _ *fakePublisher) {
			meshOptimizer.inspectErrAt = 2
		}},
		{name: "optimized stat", job: validJob, setup: func(_ *Processor, _ *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			calls := 0
			statFile = func(path string) (os.FileInfo, error) {
				calls++
				if calls == 2 {
					return nil, errors.New("stat failed")
				}
				return os.Stat(path)
			}
		}},
		{name: "hash open", job: validJob, setup: func(_ *Processor, _ *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			openFile = func(string) (*os.File, error) { return nil, errors.New("open failed") }
		}},
		{name: "hash copy", job: validJob, setup: func(_ *Processor, _ *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			copyStream = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy failed") }
		}},
		{name: "upload", job: validJob, setup: func(_ *Processor, storage *fakeStorage, _ *fakeOptimizer, _ *fakePublisher) {
			storage.uploadErr = errors.New("upload failed")
		}},
		{name: "fail publication", job: func() *apiv1.MeshOptimizationJob {
			job := validJob()
			job.JobId = ""
			return job
		}, setup: func(_ *Processor, _ *fakeStorage, _ *fakeOptimizer, publisher *fakePublisher) {
			publisher.failErr = errors.New("publish failed")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetHandlerOps(t)
			processor, storage, meshOptimizer, publisher := newTestProcessor(t, t.TempDir())
			if test.setup != nil {
				test.setup(processor, storage, meshOptimizer, publisher)
			}
			if err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, test.job())); err == nil {
				t.Fatal("expected processing error")
			}
		})
	}
}

func TestProcessJobCleanupAndPublicationErrors(t *testing.T) {
	resetHandlerOps(t)
	processor, _, _, publisher := newTestProcessor(t, t.TempDir())
	processor.workDirs = &fakeWorkDirs{root: t.TempDir(), cleanupErr: errors.New("cleanup failed")}
	publisher.progressErr = errors.New("progress failed")
	if err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, validJob())); err != nil {
		t.Fatalf("progress and cleanup failures are non-fatal: %v", err)
	}

	processor, _, _, publisher = newTestProcessor(t, t.TempDir())
	publisher.completeErr = errors.New("complete failed")
	if err := processor.HandleMeshJob(context.Background(), mustMarshalJob(t, validJob())); err == nil {
		t.Fatal("expected complete publication error")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	publisher.progressErr = errors.New("ignored after cancellation")
	processor.publishProgress(canceled, validJob(), meshProgress{1, 1, apiv1.MeshOptimizationStage_MESH_OPTIMIZATION_STAGE_DOWNLOADING})
}
