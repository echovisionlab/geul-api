package transcode

import (
	"context"
	"errors"
	"time"
)

const (
	StatusQueued         = "TRANSCODE_JOB_STATUS_QUEUED"
	StatusProcessing     = "TRANSCODE_JOB_STATUS_PROCESSING"
	StatusCompleted      = "TRANSCODE_JOB_STATUS_COMPLETED"
	StatusFailedTerminal = "TRANSCODE_JOB_STATUS_FAILED_TERMINAL"
	StatusCancelled      = "TRANSCODE_JOB_STATUS_CANCELLED"
)

const (
	WaveformJobStatusQueued    = "WAVEFORM_JOB_STATUS_QUEUED"
	WaveformJobStatusCompleted = "WAVEFORM_JOB_STATUS_COMPLETED"
	WaveformJobStatusFailed    = "WAVEFORM_JOB_STATUS_FAILED"
	WaveformJobStatusCancelled = "WAVEFORM_JOB_STATUS_CANCELLED"
)

// ErrJobNotFound is the Store's answer for a missing transcode job.
var ErrJobNotFound = errors.New("transcode job not found")

// Job is the queryable state of one transcode command. Payload is the
// immutable encoded command; PGMQ owns its delivery.
type Job struct {
	EventID             string
	QueueName           string
	EntityType          string
	EntityID            string
	FileID              string
	Payload             []byte
	Status              string
	Progress            int
	HLSProgress         int
	SpectrogramProgress int
	LastSequence        *int64
	LastStage           *string
	LastError           *string
	CompletedAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Progress is a job's progress after one progress event. A nil
// LastSequence or LastStage keeps the stored value.
type Progress struct {
	Progress            int
	HLSProgress         int
	SpectrogramProgress int
	LastSequence        *int64
	LastStage           *string
}

// CompletionIdentity names the job a completion event reports on.
type CompletionIdentity struct {
	EventID    string
	QueueName  string
	EntityType string
	EntityID   string
	FileID     string
}

// WaveformJob is the queryable state of one waveform command.
type WaveformJob struct {
	EventID         string
	EntityType      string
	EntityID        string
	FileID          string
	Status          string
	Progress        int
	CancelRequested bool
	LastError       *string
	CompletedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Store is the transcode and waveform job state, bound to one transaction.
type Store interface {
	// InsertJob stores job unless a job with its event ID exists, and
	// reports whether it did.
	InsertJob(ctx context.Context, job Job) (bool, error)
	// CancelOtherActiveJobs cancels the active jobs of fileID on queueName
	// other than eventID.
	CancelOtherActiveJobs(ctx context.Context, eventID, queueName, fileID, reason string, now time.Time) error
	// LockJob locks a job FOR UPDATE.
	LockJob(ctx context.Context, eventID string) (Job, error)
	// LockUnsettledJob locks a job that is neither completed, cancelled nor
	// failed, FOR UPDATE.
	LockUnsettledJob(ctx context.Context, eventID string) (Job, bool, error)
	// RecordProgress marks the job processing with progress.
	RecordProgress(ctx context.Context, eventID string, progress Progress, now time.Time) error
	// CompletionCandidates returns the jobs a completion names, newest first.
	CompletionCandidates(ctx context.Context, identity CompletionIdentity) ([]Job, error)
	DeleteJob(ctx context.Context, eventID string) error
	FailJob(ctx context.Context, eventID, reason string, now time.Time) error
	// CancelFileJobs cancels the active jobs of a File.
	CancelFileJobs(ctx context.Context, fileID, reason string, now time.Time) error
	// QueuedTrackWaveformJobs returns the queued waveform jobs of Tracks.
	QueuedTrackWaveformJobs(ctx context.Context, trackIDs []string) ([]WaveformJob, error)
	// CancelWaveformJobs marks waveform jobs cancelled at the request of
	// their owner.
	CancelWaveformJobs(ctx context.Context, eventIDs []string, reason string, now time.Time) error
}

// ActiveStatuses are the statuses of a job whose result may still change
// its File.
func ActiveStatuses() []string {
	return []string{StatusQueued, StatusProcessing}
}
