// Package handler coordinates media transcode commands and their derivatives.
package handler

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobregistry"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Options configures a transcode handler and its collaborators.
type Options struct {
	JobTimeoutMinutes int
	AudioHLSBitrate   string
	FFmpeg            FFmpegExecutor
	Storage           StorageClient
	Publisher         EventPublisher
}

// Handler coordinates audio and video transcode jobs.
type Handler struct {
	jobs  *jobregistry.Registry
	audio *audioProcessor
	video *videoProcessor
}

// NewHandler validates dependencies and builds a transcode handler.
func NewHandler(options Options) (*Handler, error) {
	if err := validateHandlerOptions(options); err != nil {
		return nil, err
	}

	jobs := &jobregistry.Registry{}
	completions := &completionService{storage: options.Storage, publisher: options.Publisher}
	coordinator := &jobCoordinator{
		jobs:        jobs,
		completions: completions,
		progress:    progressPublisher{publisher: options.Publisher},
	}
	sources := &sourcePreparer{
		workDirs:          options.FFmpeg,
		inspector:         options.FFmpeg,
		storage:           options.Storage,
		jobTimeoutMinutes: options.JobTimeoutMinutes,
	}

	return &Handler{
		jobs: jobs,
		audio: &audioProcessor{
			coordinator: coordinator,
			sources:     sources,
			generator:   options.FFmpeg,
			completions: completions,
			storage:     options.Storage,
			bitrate:     options.AudioHLSBitrate,
		},
		video: &videoProcessor{
			coordinator: coordinator,
			sources:     sources,
			generator:   options.FFmpeg,
			completions: completions,
			storage:     options.Storage,
		},
	}, nil
}

func validateHandlerOptions(options Options) error {
	if options.JobTimeoutMinutes <= 0 {
		return fmt.Errorf("job timeout minutes must be positive")
	}
	if options.AudioHLSBitrate == "" {
		return fmt.Errorf("audio HLS bitrate is required")
	}
	if options.FFmpeg == nil {
		return fmt.Errorf("FFmpeg executor is required")
	}
	if options.Storage == nil {
		return fmt.Errorf("storage client is required")
	}
	if options.Publisher == nil {
		return fmt.Errorf("event publisher is required")
	}
	return nil
}

// HandleAudioJob processes one audio transcode command.
func (h *Handler) HandleAudioJob(ctx context.Context, job *apiv1.TranscodeAudioEvent) error {
	return h.audio.process(ctx, job)
}

// HandleVideoJob processes one video transcode command.
func (h *Handler) HandleVideoJob(ctx context.Context, job *apiv1.TranscodeVideoEvent) error {
	return h.video.process(ctx, job)
}

// CancelJob cancels the active job for a file and reports whether one existed.
func (h *Handler) CancelJob(fileID string) bool {
	return h.jobs.CancelGroup(fileID)
}
