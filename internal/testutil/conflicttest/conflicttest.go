// Package conflicttest checks how a domain reports a revision conflict found
// while storing: the client sees a typed collaboration conflict, and the
// domain's own errors.Is/As branches still see the raw conflict.
package conflicttest

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// RequireStoreConflictsKeepCause feeds a stale document revision and a target
// revision conflict to normalize, the domain's content block error mapper.
func RequireStoreConflictsKeepCause(t *testing.T, normalize func(error) error) {
	t.Helper()
	current := uuid.New()
	stale := normalize(&contentblock.StaleRevisionError{CurrentRevision: current})
	detail := requireConflict(t, stale, intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED)
	if detail.GetCurrentDocumentRevision() != current.String() {
		t.Fatalf("current document revision = %q, want %q", detail.GetCurrentDocumentRevision(), current)
	}
	var staleCause *contentblock.StaleRevisionError
	if !errors.As(stale, &staleCause) || !errors.Is(stale, contentblock.ErrStaleRevision) {
		t.Fatalf("normalized stale revision lost its cause: %v", stale)
	}

	target := normalize(&translation.TargetRevisionConflict{CurrentRevision: "tr1_current", CurrentExists: true})
	detail = requireConflict(t, target, intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED)
	if detail.GetCurrentTargetRevision() != "tr1_current" || !detail.GetCurrentTargetExists() {
		t.Fatalf("target conflict detail = %v", detail)
	}
	var targetCause *translation.TargetRevisionConflict
	if !errors.As(target, &targetCause) {
		t.Fatalf("normalized target conflict lost its cause: %v", target)
	}
}

func requireConflict(
	t *testing.T,
	err error,
	reason intrav1.CollaborationConflictReason,
) *intrav1.CollaborationConflictDetail {
	t.Helper()
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want %v (%v)", got, connect.CodeFailedPrecondition, err)
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || len(connectErr.Details()) != 1 {
		t.Fatalf("error %v does not carry one conflict detail", err)
	}
	value, decodeErr := connectErr.Details()[0].Value()
	if decodeErr != nil {
		t.Fatalf("decode conflict detail: %v", decodeErr)
	}
	detail, ok := value.(*intrav1.CollaborationConflictDetail)
	if !ok || detail.GetReason() != reason {
		t.Fatalf("detail = %v, want reason %v", value, reason)
	}
	return detail
}
