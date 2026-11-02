package menu

import (
	"github.com/echovisionlab/geul-api/internal/domainerr"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// The collaboration answers carry the generated reason enums domainerr
// defines their details with.

func targetRoomStructureRejected() error {
	return domainerr.CollaborationRejection(
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
		"Menu target room cannot change shared structure",
	)
}

func roomDocumentRevisionChanged(previous AIDocumentSnapshot) error {
	return domainerr.CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
		"Menu document changed; reload before saving", domainerr.CollaborationRevisions{Document: previous.DocumentRevision},
	)
}

func roomTargetRevisionChanged(previous AIDocumentSnapshot) error {
	return domainerr.CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
		"Menu target changed; reload before saving",
		domainerr.CollaborationRevisions{Document: previous.DocumentRevision, Target: *previous.TargetRevision, TargetExists: &previous.LocaleExists},
	)
}
