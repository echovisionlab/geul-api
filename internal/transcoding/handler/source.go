package handler

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/echovisionlab/geul-api/internal/transcoding/ffmpeg"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type sourceValidator func(context.Context, string) error

type sourcePreparer struct {
	workDirs          WorkDirManager
	inspector         MediaInspector
	storage           StorageClient
	jobTimeoutMinutes int
}

type preparedSource struct {
	workDir string
	path    string
	probe   *ffmpeg.ProbeResult
}

func (p *sourcePreparer) prepareJobSource(
	ctx context.Context,
	jobID string,
	objectKey string,
	reportProgress progressReporter,
	validate sourceValidator,
) (*preparedSource, error) {
	workDir, err := p.workDirs.CreateJobWorkDir(jobID)
	if err != nil {
		return nil, err
	}
	sourcePath, probe, err := p.prepareSource(ctx, workDir, objectKey, reportProgress, validate)
	if err != nil {
		p.cleanupJobWorkDir(jobID)
		return nil, err
	}
	return &preparedSource{workDir: workDir, path: sourcePath, probe: probe}, nil
}

func (p *sourcePreparer) prepareSource(
	ctx context.Context,
	workDir string,
	objectKey string,
	reportProgress progressReporter,
	validate sourceValidator,
) (string, *ffmpeg.ProbeResult, error) {
	sourcePath := filepath.Join(workDir, "source")
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING, 0)
	if err := p.storage.Download(ctx, objectKey, sourcePath); err != nil {
		return "", nil, fmt.Errorf("download failed: %w", err)
	}
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING, 100)

	if err := validate(ctx, sourcePath); err != nil {
		return "", nil, err
	}
	probe, err := p.inspector.Probe(ctx, sourcePath)
	if err != nil {
		return "", nil, fmt.Errorf("probe failed: %w", err)
	}
	return sourcePath, probe, nil
}

func (p *sourcePreparer) withTranscodeTimeout(
	ctx context.Context,
	jobID string,
	durationSeconds float64,
) (context.Context, context.CancelFunc) {
	timeout := ffmpeg.CalculateTimeout(durationSeconds, p.jobTimeoutMinutes)
	slog.Debug("Applied dynamic timeout",
		"job_id", jobID,
		"media_duration", durationSeconds,
		"timeout", timeout,
	)
	return context.WithTimeout(ctx, timeout)
}

func (p *sourcePreparer) cleanupJobWorkDir(jobID string) {
	if err := p.workDirs.CleanupJobWorkDir(jobID); err != nil {
		slog.Warn("Failed to clean transcoder work directory", "job_id", jobID, "error", err)
	}
}
