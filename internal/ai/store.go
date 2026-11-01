package ai

import (
	"context"
	"errors"
	"time"
)

// JobStatus is a stored metadata job status.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobReady     JobStatus = "ready"
	JobFailed    JobStatus = "failed"
	JobApplied   JobStatus = "applied"
	JobDismissed JobStatus = "dismissed"
)

// Target is the resource a metadata job writes suggestions for. Type is
// the stored resource type name (AI_RESOURCE_TYPE_POST).
type Target struct {
	Type string
	ID   string
}

// The resource types metadata generation decides by.
const (
	TargetPost   = "AI_RESOURCE_TYPE_POST"
	TargetWork   = "AI_RESOURCE_TYPE_WORK"
	TargetArtist = "AI_RESOURCE_TYPE_ARTIST"
)

// Resolution is how a requester resolves a ready job; anything else is
// unsupported.
type Resolution string

const (
	ResolutionApplied   Resolution = "applied"
	ResolutionDismissed Resolution = "dismissed"
)

// Job is one durable metadata generation request and its outcome.
type Job struct {
	ID                string
	RequesterMemberID string
	TargetType        string
	TargetID          string
	RequestedKeys     []string
	Context           string
	Prompt            string
	Status            JobStatus
	Suggestion        map[string]string
	ResponseText      *string
	Error             *string
	Provider          *string
	Model             *string
	DurationMS        *int64
	StartedAt         *time.Time
	CompletedAt       *time.Time
	ResolvedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Lease names a running job as one delivery started it: a stored start
// that changed means another delivery holds the job.
type Lease struct {
	JobID     string
	StartedAt time.Time
}

// JobOutcome is what a delivery stores when it ends a running job.
type JobOutcome struct {
	Status       JobStatus
	Suggestion   map[string]string
	ResponseText *string
	Error        *string
	DurationMS   int64
	CompletedAt  time.Time
}

// Store is what the metadata use cases and job runner read and write,
// bound to one transaction or read snapshot. aipostgres implements it over
// metadata_ai_job and the PGMQ queue; aitest is the in-memory fake.
type Store interface {
	// InsertJob stores a new queued job and returns it as stored.
	InsertJob(ctx context.Context, job Job) (Job, error)
	// Job reads a job, or ErrJobNotFound.
	Job(ctx context.Context, jobID string) (Job, error)
	// ResolveJob moves a ready job to status; it answers whether the job
	// was still ready.
	ResolveJob(ctx context.Context, jobID string, status JobStatus, now time.Time) (bool, error)
	// ClaimJob moves a queued job, or a running job still started as job
	// says, to running for provider and model; it answers whether it won.
	ClaimJob(ctx context.Context, job Job, provider, model string, startedAt time.Time) (bool, error)
	// FinishJob stores outcome on the job lease still names; it answers
	// whether the lease held.
	FinishJob(ctx context.Context, lease Lease, outcome JobOutcome) (bool, error)
	// LockExpiredJobs locks FOR UPDATE SKIP LOCKED, oldest first, up to
	// limit queued jobs untouched since recoveryBefore and running jobs
	// untouched since recoveryBefore that started before staleBefore.
	LockExpiredJobs(ctx context.Context, recoveryBefore, staleBefore time.Time, limit int) ([]Job, error)
	// TransportPending answers whether the queue still holds the job's
	// generate command.
	TransportPending(ctx context.Context, jobID string) (bool, error)
	// TouchExpiredJob moves an expired job's updated_at to now unless
	// another delivery changed it since job was read; it answers whether
	// it did.
	TouchExpiredJob(ctx context.Context, job Job, now time.Time) (bool, error)
	// EnqueueGenerate enqueues the job's generate command in this
	// transaction.
	EnqueueGenerate(ctx context.Context, jobID string) error
}

// ErrJobNotFound is a job that does not exist.
var ErrJobNotFound = errors.New("metadata AI job not found")
