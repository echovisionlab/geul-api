package application

import "time"

// JobStatus is the state of a Translation job. A job row exists only while it
// is queued or running; the terminal statuses name how a deleted row ended.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobApplied   JobStatus = "applied"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Job is one explicitly requested in-flight generation of a target locale.
// RequestXLIFF and RequestManifest are the persisted request artifact; the
// runner reads them through Store.RequestArtifact, so a job read for its
// state leaves them empty.
type Job struct {
	ID                          string
	EntityType                  string
	EntityID                    string
	TargetLocale                string
	SourceLocale                string
	RequestArtifactDigest       string
	OperationID                 string
	Status                      JobStatus
	RequestedByMemberID         string
	Provider                    *string
	Model                       *string
	ProviderDocumentID          *string
	ProviderDocumentKey         *string
	ProviderDocumentSubmittedAt *time.Time
	RequestXLIFF                []byte
	RequestManifest             []byte
	// FailureReason and CompletedAt describe how the job ended; they are
	// never stored.
	FailureReason *string
	RequestedAt   time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ActiveJobKey names the active jobs one regeneration request can reuse: the
// same entity, target locale and request artifact.
type ActiveJobKey struct {
	EntityType            string
	EntityID              string
	TargetLocale          string
	RequestArtifactDigest string
}

// ProviderDocument is a provider's handle on an uploaded document, stored on
// the running job so a redelivery resumes it.
type ProviderDocument struct {
	Provider    string
	Model       string
	DocumentID  string
	DocumentKey string
	SubmittedAt time.Time
}

// StoredRequestArtifact is the request artifact as stored, before the
// manifest is canonicalized and the digest checked.
type StoredRequestArtifact struct {
	XLIFF    []byte
	Manifest []byte
	Digest   string
}
