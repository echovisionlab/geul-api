package handler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"github.com/echovisionlab/geul-api/internal/assetprocessing/optimizer"
)

type pipelineResult struct {
	before       optimizer.Inspection
	after        optimizer.Inspection
	inputSize    int64
	outputSize   int64
	outputSHA256 []byte
}

type downloadedSource struct {
	path string
	size int64
}

type pipelineFailure struct {
	reason apiv1.MeshOptimizationFailureReason
	cause  error
}

func (e *pipelineFailure) Error() string { return e.cause.Error() }
func (e *pipelineFailure) Unwrap() error { return e.cause }

var (
	statFile   = os.Stat
	openFile   = os.Open
	copyStream = io.Copy
)

func (p *Processor) runPipeline(
	ctx context.Context,
	job *apiv1.MeshOptimizationJob,
	spec meshJobSpec,
) (*pipelineResult, error) {
	workDir, err := p.workDirs.CreateJobWorkDir(spec.jobID)
	if err != nil {
		return nil, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_INTERNAL, "create work dir", err)
	}
	defer func() {
		if cleanupErr := p.workDirs.CleanupJobWorkDir(workDir); cleanupErr != nil {
			slog.Warn("Failed to cleanup mesh optimization work dir", "jobId", spec.jobID, "workDir", workDir, "error", cleanupErr)
		}
	}()

	source, err := p.downloadSource(ctx, job, spec, workDir)
	if err != nil {
		return nil, err
	}
	result, err := p.optimizeSource(ctx, job, spec, workDir, source)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (p *Processor) downloadSource(
	ctx context.Context,
	job *apiv1.MeshOptimizationJob,
	spec meshJobSpec,
	workDir string,
) (downloadedSource, error) {
	p.publishProgress(ctx, job, downloadStarted)
	remoteSize, err := p.storage.GetObjectSize(ctx, spec.sourceKey)
	if err != nil {
		return downloadedSource{}, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_SOURCE_NOT_FOUND, "inspect source object", err)
	}
	if remoteSize > p.config.MaxInputBytes {
		return downloadedSource{}, pipelineRejected("input GLB is %d bytes; max is %d", remoteSize, p.config.MaxInputBytes)
	}

	inputPath := filepath.Join(workDir, "source.glb")
	if err := p.storage.Download(ctx, spec.sourceKey, inputPath); err != nil {
		return downloadedSource{}, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_DOWNLOAD_FAILED, "download source GLB", err)
	}
	inputStat, err := statFile(inputPath)
	if err != nil {
		return downloadedSource{}, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_DOWNLOAD_FAILED, "stat downloaded source", err)
	}
	if inputStat.Size() > p.config.MaxInputBytes {
		return downloadedSource{}, pipelineRejected("downloaded GLB is %d bytes; max is %d", inputStat.Size(), p.config.MaxInputBytes)
	}
	return downloadedSource{path: inputPath, size: inputStat.Size()}, nil
}

func (p *Processor) optimizeSource(
	ctx context.Context,
	job *apiv1.MeshOptimizationJob,
	spec meshJobSpec,
	workDir string,
	source downloadedSource,
) (*pipelineResult, error) {
	p.publishProgress(ctx, job, analysisStarted)
	before, err := p.optimizer.Inspect(ctx, source.path)
	if err != nil {
		return nil, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED, "inspect source GLB", err)
	}

	outputPath := filepath.Join(workDir, "optimized.glb")
	p.publishProgress(ctx, job, optimizationStarted)
	if err := p.optimizer.Optimize(ctx, source.path, outputPath, spec.options); err != nil {
		return nil, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED, "optimize GLB", err)
	}

	p.publishProgress(ctx, job, outputAnalysisStarted)
	after, err := p.optimizer.Inspect(ctx, outputPath)
	if err != nil {
		return nil, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED, "inspect optimized GLB", err)
	}
	outputStat, err := statFile(outputPath)
	if err != nil {
		return nil, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED, "stat optimized GLB", err)
	}
	outputSHA256, err := fileSHA256(outputPath)
	if err != nil {
		return nil, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_OPTIMIZATION_FAILED, "hash optimized GLB", err)
	}

	p.publishProgress(ctx, job, uploadStarted)
	if err := p.storage.Upload(ctx, spec.outputKey, outputPath, spec.outputTarget.GetMimeType()); err != nil {
		return nil, pipelineFailed(apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_UPLOAD_FAILED, "upload optimized GLB", err)
	}

	return &pipelineResult{
		before:       before,
		after:        after,
		inputSize:    source.size,
		outputSize:   outputStat.Size(),
		outputSHA256: outputSHA256,
	}, nil
}

func pipelineFailed(reason apiv1.MeshOptimizationFailureReason, operation string, err error) *pipelineFailure {
	return &pipelineFailure{reason: reason, cause: fmt.Errorf("%s: %w", operation, err)}
}

func pipelineRejected(message string, args ...any) *pipelineFailure {
	return &pipelineFailure{
		reason: apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED,
		cause:  fmt.Errorf(message, args...),
	}
}

func fileSHA256(filePath string) ([]byte, error) {
	file, err := openFile(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := copyStream(hash, file); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}
