package form

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestFormDocumentRevisionErrorsCarryTypedCollaborationConflicts(t *testing.T) {
	testCases := []struct {
		name   string
		err    error
		reason intrav1.CollaborationConflictReason
	}{
		{
			name:   "source document revision",
			err:    formDocumentRevisionChangedConflict(),
			reason: intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
		},
		{
			name:   "existing target revision",
			err:    normalizeFormDocumentSaveError(&translation.TargetRevisionConflict{CurrentRevision: "tr1_current", CurrentExists: true}),
			reason: intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
		},
		{
			name:   "missing target row",
			err:    normalizeFormDocumentSaveError(&translation.TargetRevisionConflict{}),
			reason: intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
		},
		{
			name:   "postgres serialization failure",
			err:    normalizeFormDocumentSaveError(fmt.Errorf("save form document: %w", &pgconn.PgError{Code: "40001"})),
			reason: intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := connect.CodeOf(testCase.err); got != connect.CodeFailedPrecondition {
				t.Fatalf("code = %v, want %v", got, connect.CodeFailedPrecondition)
			}
			connectErr, ok := testCase.err.(*connect.Error)
			if !ok {
				t.Fatalf("error type = %T, want *connect.Error", testCase.err)
			}
			if len(connectErr.Details()) != 1 {
				t.Fatalf("details = %d, want 1", len(connectErr.Details()))
			}
			value, err := connectErr.Details()[0].Value()
			if err != nil {
				t.Fatalf("decode conflict detail: %v", err)
			}
			detail, ok := value.(*intrav1.CollaborationConflictDetail)
			if !ok {
				t.Fatalf("detail type = %T, want *CollaborationConflictDetail", value)
			}
			if detail.GetReason() != testCase.reason {
				t.Fatalf("reason = %v, want %v", detail.GetReason(), testCase.reason)
			}
		})
	}
}

func TestNormalizeFormDocumentSaveErrorPreservesUnrelatedErrors(t *testing.T) {
	want := errors.New("storage unavailable")
	if got := normalizeFormDocumentSaveError(want); got != want {
		t.Fatalf("normalized error = %v, want original error", got)
	}

	want = fmt.Errorf("save form document: %w", &pgconn.PgError{Code: "40P01"})
	if got := normalizeFormDocumentSaveError(want); got != want {
		t.Fatalf("deadlock error = %v, want original error", got)
	}
}
