package collaborationconflict

import (
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/require"
)

func TestConflictsCarryTheirMachineReadableDetail(t *testing.T) {
	conflict, ok := errs.Classified(DocumentRevisionChanged("theme changed; reload before saving", "7"))
	require.True(t, ok)
	require.Equal(t, connect.CodeFailedPrecondition, conflict.Code())
	require.Equal(t, "theme changed; reload before saving", conflict.Message())
	require.Len(t, conflict.Details(), 1)
	detail, err := conflict.Details()[0].Value()
	require.NoError(t, err)
	state := detail.(*intrav1.CollaborationConflictDetail)
	require.Equal(t, intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, state.Reason)
	require.Equal(t, "7", state.GetCurrentDocumentRevision())

	rejection, ok := errs.Classified(MutationRejection(intrav1.CollaborationMutationRejectionReason(1), "rejected"))
	require.True(t, ok)
	require.Equal(t, connect.CodeInvalidArgument, rejection.Code())
	require.Len(t, rejection.Details(), 1)
}
