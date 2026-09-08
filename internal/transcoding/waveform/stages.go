package waveform

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

type progressStep struct {
	sequence int64
	percent  int32
	stage    apiv1.TranscodeStage
}

type progressMilestone uint8

const (
	downloadStarted progressMilestone = iota
	downloadFinished
	peaksStarted
	peaksFinished
	uploadStarted
	uploadFinished
)

func (milestone progressMilestone) step() progressStep {
	switch milestone {
	case downloadStarted:
		return progressStep{sequence: 1, percent: 5, stage: apiv1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING}
	case downloadFinished:
		return progressStep{sequence: 2, percent: 20, stage: apiv1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING}
	case peaksStarted:
		return progressStep{sequence: 3, percent: 45, stage: apiv1.TranscodeStage_TRANSCODE_STAGE_WAVEFORM_PROCESSING}
	case peaksFinished:
		return progressStep{sequence: 4, percent: 80, stage: apiv1.TranscodeStage_TRANSCODE_STAGE_WAVEFORM_PROCESSING}
	case uploadStarted:
		return progressStep{sequence: 5, percent: 90, stage: apiv1.TranscodeStage_TRANSCODE_STAGE_UPLOADING}
	case uploadFinished:
		return progressStep{sequence: 6, percent: 100, stage: apiv1.TranscodeStage_TRANSCODE_STAGE_UPLOADING}
	default:
		panic("unknown waveform progress milestone")
	}
}

func (p *Processor) createWorkDir(event *apiv1.WaveformGenerateEvent, startedAt time.Time) (string, error) {
	workDir, err := p.workDirs.CreateJobWorkDir("waveform-" + event.EventId)
	if err != nil {
		return "", fmt.Errorf("create work dir: %w", err)
	}
	slog.Info("Waveform work directory created",
		"job_id", event.EventId,
		"work_dir", workDir,
		"elapsed_ms", time.Since(startedAt).Milliseconds(),
	)
	return workDir, nil
}

func (p *Processor) cleanupWorkDir(eventID string) {
	if err := p.workDirs.CleanupJobWorkDir("waveform-" + eventID); err != nil {
		slog.Warn("Failed to cleanup waveform work dir", "job_id", eventID, "error", err)
	}
}

func (p *Processor) downloadSource(
	ctx context.Context,
	event *apiv1.WaveformGenerateEvent,
	workDir string,
	startedAt time.Time,
) (string, error) {
	source := event.GetSource()
	sourcePath := filepath.Join(workDir, "source."+source.GetExtension())
	p.publishProgress(ctx, event, downloadStarted.step())
	slog.Info("Waveform source download started",
		"job_id", event.EventId,
		"source_path", sourcePath,
		"source_key", source.GetObjectKey(),
	)
	if err := p.storage.Download(ctx, source.GetObjectKey(), sourcePath); err != nil {
		return "", fmt.Errorf("download source asset: %w", err)
	}
	slog.Info("Waveform source download completed",
		"job_id", event.EventId,
		"elapsed_ms", time.Since(startedAt).Milliseconds(),
	)
	p.publishProgress(ctx, event, downloadFinished.step())
	return sourcePath, nil
}

func (p *Processor) generatePeaks(
	ctx context.Context,
	event *apiv1.WaveformGenerateEvent,
	sourcePath string,
	startedAt time.Time,
) ([][]float64, error) {
	p.publishProgress(ctx, event, peaksStarted.step())
	slog.Info("Waveform peak generation started", "job_id", event.EventId, "source_path", sourcePath)
	peaks, err := p.generator.GeneratePeaks(ctx, sourcePath)
	if err != nil {
		return nil, fmt.Errorf("extract waveform: %w", err)
	}

	channels := len(peaks)
	points := 0
	if channels > 0 {
		points = len(peaks[0])
	}
	slog.Info("Waveform peak generation completed",
		"job_id", event.EventId,
		"channels", channels,
		"points", points,
		"elapsed_ms", time.Since(startedAt).Milliseconds(),
	)
	if channels == 0 || points == 0 {
		return nil, fmt.Errorf("extract waveform: empty peaks")
	}
	p.publishProgress(ctx, event, peaksFinished.step())
	return peaks, nil
}

func writeWaveformPayload(workDir string, eventID string, peaks [][]float64) ([]byte, string, error) {
	payload, err := json.Marshal(peaks)
	if err != nil {
		return nil, "", fmt.Errorf("marshal waveform: %w", err)
	}
	path := filepath.Join(workDir, "waveform.json")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		return nil, "", fmt.Errorf("write waveform file: %w", err)
	}
	slog.Info("Waveform payload written", "job_id", eventID, "waveform_path", path, "points", len(peaks[0]))
	return payload, path, nil
}

func (p *Processor) uploadWaveform(
	ctx context.Context,
	event *apiv1.WaveformGenerateEvent,
	path string,
	payload []byte,
	startedAt time.Time,
) (*apiv1.WaveformCompleteEvent, error) {
	output := event.GetOutput()
	p.publishProgress(ctx, event, uploadStarted.step())
	slog.Info("Waveform upload started", "job_id", event.EventId, "waveform_key", output.GetObjectKey())
	complete := &apiv1.WaveformCompleteEvent{
		EventId:          event.EventId,
		EntityType:       event.EntityType,
		EntityId:         event.EntityId,
		FileId:           event.FileId,
		Output:           waveformWriteResult(output, payload),
		ProcessingTimeMs: time.Since(startedAt).Milliseconds(),
		TimestampMs:      time.Now().UnixMilli(),
	}
	completion, err := (proto.MarshalOptions{Deterministic: true}).Marshal(complete)
	if err != nil {
		return nil, fmt.Errorf("encode waveform completion: %w", err)
	}
	if err := p.storage.UploadCompleted(ctx, output.GetObjectKey(), path, output.GetMimeType(), completion); err != nil {
		return nil, fmt.Errorf("upload waveform asset: %w", err)
	}
	slog.Info("Waveform upload completed",
		"job_id", event.EventId,
		"waveform_key", output.GetObjectKey(),
		"elapsed_ms", time.Since(startedAt).Milliseconds(),
	)
	p.publishProgress(ctx, event, uploadFinished.step())
	return complete, nil
}

func (p *Processor) failUnlessCancelled(
	jobCtx context.Context,
	publishCtx context.Context,
	event *apiv1.WaveformGenerateEvent,
	startedAt time.Time,
	cause error,
) error {
	if p.isCancelled(jobCtx, cause) {
		return nil
	}
	return p.publishFail(publishCtx, event, startedAt, cause)
}
