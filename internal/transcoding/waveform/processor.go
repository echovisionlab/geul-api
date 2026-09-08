// Package waveform processes waveform generation commands.
package waveform

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobregistry"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// WorkDirManager owns waveform job directories.
type WorkDirManager interface {
	CreateJobWorkDir(jobID string) (string, error)
	CleanupJobWorkDir(jobID string) error
}

// PeakGenerator extracts waveform peaks from a source file.
type PeakGenerator interface {
	GeneratePeaks(ctx context.Context, inputPath string) ([][]float64, error)
}

// StorageClient transfers waveform assets and completion receipts.
type StorageClient interface {
	Download(ctx context.Context, key string, localPath string) error
	Upload(ctx context.Context, key string, localPath string, contentType string) error
	UploadCompleted(ctx context.Context, key string, localPath string, contentType string, completion []byte) error
	Completion(ctx context.Context, key string) ([]byte, bool, error)
}

// EventPublisher emits waveform progress and terminal events.
type EventPublisher interface {
	PublishWaveformProgress(ctx context.Context, event *apiv1.WaveformProgressEvent) error
	PublishWaveformComplete(ctx context.Context, event *apiv1.WaveformCompleteEvent) error
	PublishWaveformFail(ctx context.Context, event *apiv1.WaveformFailEvent) error
}

// Processor coordinates waveform generation jobs.
type Processor struct {
	workDirs  WorkDirManager
	generator PeakGenerator
	storage   StorageClient
	publisher EventPublisher
	jobs      jobregistry.Registry
}

// Options configures waveform processing dependencies.
type Options struct {
	WorkDirs  WorkDirManager
	Generator PeakGenerator
	Storage   StorageClient
	Publisher EventPublisher
}

// NewProcessor validates dependencies and builds a waveform processor.
func NewProcessor(options Options) (*Processor, error) {
	if options.WorkDirs == nil {
		return nil, fmt.Errorf("work dir manager is required")
	}
	if options.Generator == nil {
		return nil, fmt.Errorf("peak generator is required")
	}
	if options.Storage == nil {
		return nil, fmt.Errorf("storage client is required")
	}
	if options.Publisher == nil {
		return nil, fmt.Errorf("event publisher is required")
	}

	return &Processor{
		workDirs:  options.WorkDirs,
		generator: options.Generator,
		storage:   options.Storage,
		publisher: options.Publisher,
	}, nil
}
