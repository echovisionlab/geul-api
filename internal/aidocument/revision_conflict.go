package aidocument

import "fmt"

// RevisionConflict carries the owning domain's current CAS tokens. Message
// preserves its diagnostic; the adapter supplies the public affected handles.
type RevisionConflict struct {
	Kind                    ConflictCode
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
	AffectedHandles         []string
	Message                 string
}

func (e *RevisionConflict) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s: current document revision is %q", e.Kind, e.CurrentDocumentRevision)
}
