package application

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ErrFileNotFound is the Store's answer for a File that no longer exists.
var ErrFileNotFound = errors.New("processing source file not found")

// Stored public asset and media generation statuses the results branch on.
const (
	AssetAllocated     = "allocated"
	AssetReady         = "ready"
	AssetFailed        = "failed"
	AssetDeletePending = "delete_pending"
	AssetDeleted       = "deleted"

	GenerationAllocated = "allocated"
	GenerationReady     = "ready"
	GenerationRetired   = "retired"
)

// Track processing statuses a result settles a Track's audio in.
const (
	TrackProcessing = "TRACK_PROCESSING_STATUS_PROCESSING"
	TrackCompleted  = "TRACK_PROCESSING_STATUS_COMPLETED"
	TrackFailed     = "TRACK_PROCESSING_STATUS_FAILED"
)

// File is the part of a processing source File results read.
type File struct {
	ID              string
	MimeType        string
	DurationSeconds *int
	IngestSlotID    *string
	IngestAttemptID *string
}

// Asset is the part of a public asset results read.
type Asset struct {
	ID           string
	SourceFileID *string
	Kind         string
	Status       string
	FileSize     *int64
	SHA256       []byte
}

// MediaGeneration is the part of a media generation results read.
type MediaGeneration struct {
	ID     string
	FileID string
	Kind   string
	Status string
}

// Derivative is one derivative of a File: an asset or a media generation.
type Derivative struct {
	ID                string
	FileID            string
	Type              string
	AssetID           *string
	MediaGenerationID *string
}

// Store is the File media state processing results apply to, and the media
// asset lifecycle they complete, bound to one transaction.
type Store interface {
	// FileAcceptsResults reports whether the File exists and is not pending
	// deletion.
	FileAcceptsResults(ctx context.Context, fileID string) (bool, error)
	// LockFileAcceptingResults locks such a File FOR UPDATE.
	LockFileAcceptingResults(ctx context.Context, fileID string) (bool, error)
	ProcessingFile(ctx context.Context, fileID string) (File, error)
	SetFileDuration(ctx context.Context, fileID string, seconds int) error
	// WaveformSource returns the canonical source of a File not pending
	// deletion, or false.
	WaveformSource(ctx context.Context, fileID string) (*commonv1.MediaObjectTarget, bool, error)

	// TranscodeJob reads the job a completion names, FOR UPDATE when lock is
	// set; nil when it does not exist.
	TranscodeJob(ctx context.Context, identity transcode.CompletionIdentity, lock bool) (*transcode.Job, error)
	// FailActiveTranscodeJob fails a queued or processing job.
	FailActiveTranscodeJob(ctx context.Context, eventID, reason string, now time.Time) (bool, error)
	// CancelActiveTranscodeJob cancels a queued or processing job.
	CancelActiveTranscodeJob(ctx context.Context, eventID, reason string, now time.Time) error
	DeleteTranscodeJob(ctx context.Context, eventID, fileID string) error
	FailedTranscodeJobs(ctx context.Context, fileID, queueName string) (int64, error)
	FileTranscodeJobs(ctx context.Context, fileID string) ([]transcode.Job, error)

	// SaveWaveformJob stores job, replacing a job with its event ID.
	SaveWaveformJob(ctx context.Context, job transcode.WaveformJob) error
	WaveformJob(ctx context.Context, eventID string) (*transcode.WaveformJob, error)
	DeleteWaveformJob(ctx context.Context, eventID string) error
	DeleteFileWaveformJob(ctx context.Context, eventID, fileID string) error
	FailWaveformJob(ctx context.Context, eventID, reason string, now time.Time) error
	CancelWaveformJob(ctx context.Context, eventID, reason string, now time.Time) error
	// RecordWaveformProgress moves a queued job's progress forward and keeps
	// the stage of its newest sequence.
	RecordWaveformProgress(ctx context.Context, eventID string, progress int, sequence int64, stage string, now time.Time) error
	QueuedWaveformJobs(ctx context.Context, fileID string) (int64, error)
	FailedWaveformJobs(ctx context.Context, fileID string) (int64, error)
	FileWaveformJobs(ctx context.Context, fileID string) ([]transcode.WaveformJob, error)

	// DerivativeAsset returns the asset of a File's derivative of type; ok is
	// false when the File has no such derivative.
	DerivativeAsset(ctx context.Context, fileID, derivativeType string) (assetID *string, ok bool, err error)
	DerivativeCount(ctx context.Context, fileID, derivativeType string) (int64, error)
	DerivativeTypes(ctx context.Context, fileID string, types []string) ([]string, error)
	// UpsertDerivative stores the File's derivative of its type, replacing
	// the one it had.
	UpsertDerivative(ctx context.Context, derivative Derivative) error
	DeleteDerivative(ctx context.Context, fileID, derivativeType string) error
	// ReadyDerivatives returns the File's derivatives whose output is ready.
	ReadyDerivatives(ctx context.Context, fileID string) ([]Derivative, error)
	AssetDerivativeCount(ctx context.Context, assetID string) (int64, error)
	WaveformAssetReferenced(ctx context.Context, assetID string) (bool, error)
	GenerationDerivativeCount(ctx context.Context, generationID string) (int64, error)

	CompleteAsset(ctx context.Context, result *commonv1.AssetWriteResult) (Asset, error)
	RequestAssetDeletion(ctx context.Context, assetID string) error
	AssetBindingCount(ctx context.Context, assetID string) (int64, error)
	Asset(ctx context.Context, assetID string) (Asset, bool, error)
	LockAsset(ctx context.Context, assetID string) (Asset, bool, error)
	// MarkAssetDeletePending marks an allocated, ready or failed asset for
	// deletion.
	MarkAssetDeletePending(ctx context.Context, assetID string, now time.Time) error
	AllocateWaveformOutput(ctx context.Context, fileID string) (*commonv1.AssetWriteTarget, error)
	CompleteMediaGeneration(ctx context.Context, result *commonv1.MediaGenerationWriteResult) (MediaGeneration, error)
	LockMediaGeneration(ctx context.Context, generationID string) (MediaGeneration, bool, error)
	// RetireMediaGeneration retires an allocated or ready generation, first
	// recording result for one that was still allocated.
	RetireMediaGeneration(ctx context.Context, generationID string, result *commonv1.MediaGenerationWriteResult, now time.Time) error

	// UpdateTrackProcessing settles the Track's audio when fileID is still
	// its source, and reports whether it was.
	UpdateTrackProcessing(ctx context.Context, trackID, fileID, status string, durationSeconds *int) (bool, error)
	TrackSourceMatches(ctx context.Context, trackID, fileID string) (bool, error)
	TrackRelease(ctx context.Context, trackID string) (string, bool, error)
}
