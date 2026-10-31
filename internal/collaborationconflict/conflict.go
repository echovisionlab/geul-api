// Package collaborationconflict holds the answers of a collaboration write
// that lost a revision race or was rejected before persistence. Use cases
// return them without knowing the transport; the Connect boundary keeps
// their machine-readable detail.
package collaborationconflict

import (
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// State is the current revision state a conflict reports to the client.
type State = errs.CollaborationState

// Conflict is the answer of a collaboration write that lost a revision race:
// a failed precondition whose detail carries reason and the state to reload.
func Conflict(reason intrav1.CollaborationConflictReason, message string, state State) error {
	return errs.CollaborationConflict(reason, message, state)
}

// DocumentRevisionChanged is the Conflict of a write whose expected document
// revision is no longer current.
func DocumentRevisionChanged(message, current string) error {
	return Conflict(intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, message, State{Document: current})
}

// MutationRejection is the answer of a collaboration write rejected before
// persistence: invalid input whose detail carries reason.
func MutationRejection(reason intrav1.CollaborationMutationRejectionReason, message string) error {
	return errs.CollaborationMutationRejection(reason, message)
}
