package handler

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/transcoding/ffmpeg"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// WorkDirManager owns per-job temporary directories.
type WorkDirManager interface {
	CreateJobWorkDir(jobID string) (string, error)
	CleanupJobWorkDir(jobID string) error
}

// MediaInspector probes and validates source media.
type MediaInspector interface {
	Probe(ctx context.Context, inputPath string) (*ffmpeg.ProbeResult, error)
	ValidateAudioFile(ctx context.Context, path string) error
	ValidateVideoFile(ctx context.Context, path string) error
}

// AudioGenerator creates audio derivatives.
type AudioGenerator interface {
	GenerateAudioHLSWithProgress(
		ctx context.Context,
		inputPath, outputDir string,
		opts ffmpeg.AudioHLSOptions,
		totalDurationSec float64,
		onProgress ffmpeg.ProgressCallback,
	) (*ffmpeg.HLSResult, error)
	GenerateAudioSpectrogramWithProgress(
		ctx context.Context,
		inputPath, outputPath string,
		opts ffmpeg.SpectrogramOptions,
		totalDurationSec float64,
		onProgress ffmpeg.ProgressCallback,
	) error
}

// VideoGenerator creates video derivatives.
type VideoGenerator interface {
	GenerateVideoThumbnail(
		ctx context.Context,
		inputPath, outputPath string,
		opts ffmpeg.VideoThumbnailOptions,
	) error
	GenerateHLSWithProgress(
		ctx context.Context,
		inputPath, outputDir string,
		opts ffmpeg.HLSOptions,
		totalDurationSec float64,
		onProgress ffmpeg.ProgressCallback,
	) (*ffmpeg.HLSResult, error)
}

// FFmpegExecutor combines the media and work-directory capabilities used by handlers.
type FFmpegExecutor interface {
	WorkDirManager
	MediaInspector
	AudioGenerator
	VideoGenerator
}

// StorageClient transfers media objects and completion receipts.
type StorageClient interface {
	Download(ctx context.Context, key string, localPath string) error
	Upload(ctx context.Context, key string, localPath string, contentType string) error
	UploadCompleted(ctx context.Context, key string, localPath string, contentType string, completion []byte) error
	Completion(ctx context.Context, key string) ([]byte, bool, error)
}

// EventPublisher emits transcode progress and terminal events.
type EventPublisher interface {
	PublishComplete(ctx context.Context, event *apiv1.TranscodeCompleteEvent) error
	PublishProgress(ctx context.Context, event *apiv1.TranscodeProgressEvent) error
}
