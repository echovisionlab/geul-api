package handler

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/echovisionlab/geul-api/internal/transcoding/ffmpeg"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (p *audioProcessor) generateSpectrogram(
	ctx context.Context,
	workDir string,
	sourcePath string,
	target *commonv1.AssetWriteTarget,
	durationSeconds float64,
	reportProgress progressReporter,
) (*commonv1.AssetWriteResult, error) {
	path := filepath.Join(workDir, "spectrogram.png")
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_PROCESSING, 0)
	if err := p.generator.GenerateAudioSpectrogramWithProgress(
		ctx,
		sourcePath,
		path,
		ffmpeg.DefaultSpectrogramOptions(),
		durationSeconds,
		func(percent int) {
			reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_PROCESSING, percent)
		},
	); err != nil {
		return nil, fmt.Errorf("audio spectrogram failed: %w", err)
	}

	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_UPLOADING, 0)
	if err := p.storage.Upload(ctx, target.GetObjectKey(), path, target.GetMimeType()); err != nil {
		return nil, fmt.Errorf("audio spectrogram upload failed: %w", err)
	}
	result, err := assetWriteResult(target, path)
	if err != nil {
		return nil, fmt.Errorf("stat spectrogram output: %w", err)
	}
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_UPLOADING, 100)
	return result, nil
}

func (p *audioProcessor) generateAudioHLS(
	ctx context.Context,
	workDir string,
	sourcePath string,
	durationSeconds float64,
	reportProgress progressReporter,
) (*ffmpeg.HLSResult, error) {
	opts := ffmpeg.DefaultAudioHLSOptions()
	opts.Bitrate = p.bitrate
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING, 0)
	result, err := p.generator.GenerateAudioHLSWithProgress(
		ctx,
		sourcePath,
		filepath.Join(workDir, "hls"),
		opts,
		durationSeconds,
		func(percent int) { reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING, percent) },
	)
	if err != nil {
		return nil, fmt.Errorf("audio hls failed: %w", err)
	}
	return result, nil
}

func videoOptions(job *apiv1.TranscodeVideoEvent) (bool, []apiv1.VideoResolution) {
	if job.GetOptions() == nil {
		return true, nil
	}
	return job.GetOptions().GetGenerateThumbnail(), job.GetOptions().GetResolutions()
}

func (p *videoProcessor) generateVideoThumbnail(
	ctx context.Context,
	workDir string,
	sourcePath string,
	target *commonv1.AssetWriteTarget,
	durationSeconds float64,
	enabled bool,
	reportProgress progressReporter,
) (*commonv1.AssetWriteResult, error) {
	if !enabled {
		return nil, nil
	}
	path := filepath.Join(workDir, "thumbnail."+target.GetExtension())
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_THUMBNAIL_PROCESSING, 0)
	if err := p.generator.GenerateVideoThumbnail(ctx, sourcePath, path, ffmpeg.VideoThumbnailOptions{
		TimeSec: int(durationSeconds / 2),
		Quality: 90,
	}); err != nil {
		return nil, fmt.Errorf("video thumbnail failed: %w", err)
	}
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_THUMBNAIL_PROCESSING, 100)
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_THUMBNAIL_UPLOADING, 0)
	if err := p.storage.Upload(ctx, target.GetObjectKey(), path, target.GetMimeType()); err != nil {
		return nil, fmt.Errorf("video thumbnail upload failed: %w", err)
	}
	result, err := assetWriteResult(target, path)
	if err != nil {
		return nil, fmt.Errorf("stat thumbnail output: %w", err)
	}
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_THUMBNAIL_UPLOADING, 100)
	return result, nil
}

func (p *videoProcessor) generateVideoHLS(
	ctx context.Context,
	jobID string,
	workDir string,
	sourcePath string,
	requested []apiv1.VideoResolution,
	durationSeconds float64,
	reportProgress progressReporter,
) (*ffmpeg.HLSResult, error) {
	opts := ffmpeg.DefaultHLSOptions()
	if resolutions := videoResolutionsToFFmpeg(requested); len(resolutions) > 0 {
		opts.Resolutions = resolutions
	}
	slog.Info("Generating HLS streams", "job_id", jobID, "resolutions", len(opts.Resolutions))
	reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING, 0)
	result, err := p.generator.GenerateHLSWithProgress(
		ctx,
		sourcePath,
		filepath.Join(workDir, "hls"),
		opts,
		durationSeconds,
		func(percent int) { reportProgress(apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING, percent) },
	)
	if err != nil {
		return nil, fmt.Errorf("video hls failed: %w", err)
	}
	return result, nil
}
