package handler

import (
	"context"
	"errors"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// JobIdentity identifies the immutable tracked allocation for a command.
type JobIdentity struct {
	EventID    string
	EntityType apiv1.TranscodeEntityType
	EntityID   string
	FileID     string
}

// JobAdmissionDecision is the worker's decision after checking durable job state.
type JobAdmissionDecision uint8

const (
	// JobAdmissionProceed means the matching job remains queued or processing.
	JobAdmissionProceed JobAdmissionDecision = iota + 1
	// JobAdmissionSettled means the allocation is already terminal or absent.
	JobAdmissionSettled
	// JobAdmissionIdentityMismatch means the event ID belongs to another command.
	JobAdmissionIdentityMismatch
)

// ErrJobAdmissionIdentityMismatch marks an event ID collision with a different
// immutable allocation. Such a message must not be allowed to start media work.
var ErrJobAdmissionIdentityMismatch = errors.New("transcode job identity mismatch")

// JobAdmission checks that a tracked command may still begin or resume work.
type JobAdmission interface {
	Admit(ctx context.Context, identity JobIdentity) (JobAdmissionDecision, error)
}
