package errors_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCollaborationConflictPreservesAuthoritativeRevisions(t *testing.T) {
	revision := uuid.New()
	for _, tc := range []struct {
		name   string
		reason intrav1.CollaborationConflictReason
		cause  error
		target string
		exists *bool
	}{
		{"source", intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, &contentblock.StaleRevisionError{CurrentRevision: revision}, "", nil},
		{"target", intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, &translation.TargetRevisionConflict{CurrentDocumentRevision: revision.String(), CurrentRevision: "target-token", CurrentExists: true}, "target-token", boolPointer(true)},
		{"deleted target", intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, &translation.TargetRevisionConflict{CurrentDocumentRevision: revision.String()}, "", boolPointer(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := errs.CollaborationConflict(tc.reason, "reload", tc.cause)
			require.Equal(t, connect.CodeFailedPrecondition, err.Code())
			require.Len(t, err.Details(), 1)
			message, decodeErr := err.Details()[0].Value()
			require.NoError(t, decodeErr)
			detail := message.(*intrav1.CollaborationConflictDetail)
			require.Equal(t, revision.String(), detail.GetCurrentDocumentRevision())
			require.Equal(t, tc.target, detail.GetCurrentTargetRevision())
			require.Equal(t, tc.exists, detail.CurrentTargetExists)
		})
	}
}
func boolPointer(value bool) *bool { return &value }
