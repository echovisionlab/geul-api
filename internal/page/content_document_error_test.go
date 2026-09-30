package page

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func TestNormalizePageContentBlockErrorMapsTargetRevisionConflict(t *testing.T) {
	err := normalizePageContentBlockError(&translation.TargetRevisionConflict{
		CurrentRevision: "tr1_current", CurrentExists: true,
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	connectErr, ok := err.(*connect.Error)
	if !ok {
		t.Fatalf("error type = %T, want *connect.Error", err)
	}
	if len(connectErr.Details()) != 1 {
		t.Fatalf("details = %d, want one typed conflict detail", len(connectErr.Details()))
	}
	value, err := connectErr.Details()[0].Value()
	if err != nil {
		t.Fatalf("decode conflict detail: %v", err)
	}
	detail, ok := value.(*intrav1.CollaborationConflictDetail)
	if !ok {
		t.Fatalf("detail type = %T, want *CollaborationConflictDetail", value)
	}
	if got, want := detail.GetReason(), intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED; got != want {
		t.Fatalf("reason = %v, want %v", got, want)
	}
}
