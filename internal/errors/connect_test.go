package errors

import (
	stderrors "errors"
	"testing"

	"connectrpc.com/connect"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func TestConnectErrorConstructors(t *testing.T) {
	tests := []struct {
		name    string
		build   func() *connect.Error
		code    connect.Code
		message string
	}{
		{name: "not authenticated", build: NotAuthenticated, code: connect.CodeUnauthenticated, message: MsgNotAuthenticated},
		{name: "authentication required", build: AuthenticationRequired, code: connect.CodeUnauthenticated, message: MsgAuthenticationRequired},
		{name: "invalid session", build: InvalidSession, code: connect.CodeUnauthenticated, message: MsgInvalidSession},
		{name: "custom permission denied", build: func() *connect.Error { return PermissionDenied("cannot publish") }, code: connect.CodePermissionDenied, message: "cannot publish"},
		{name: "admin required", build: AdminRequired, code: connect.CodePermissionDenied, message: MsgAdminRequired},
		{name: "author required", build: AuthorRequired, code: connect.CodePermissionDenied, message: MsgAuthorRequired},
		{name: "account banned", build: AccountBanned, code: connect.CodePermissionDenied, message: MsgAccountBanned},
		{name: "no permission", build: func() *connect.Error { return NoPermission("edit", "post") }, code: connect.CodePermissionDenied, message: "no permission to edit this post"},
		{name: "not found", build: func() *connect.Error { return NotFound("post", "post-1") }, code: connect.CodeNotFound, message: "post not found: post-1"},
		{name: "custom not found", build: func() *connect.Error { return NotFoundMsg("missing post") }, code: connect.CodeNotFound, message: "missing post"},
		{name: "already exists", build: func() *connect.Error { return AlreadyExists("post", "slug", "hello") }, code: connect.CodeAlreadyExists, message: "post with slug 'hello' already exists"},
		{name: "slug already exists", build: func() *connect.Error { return SlugAlreadyExists("page", "home") }, code: connect.CodeAlreadyExists, message: "page with slug 'home' already exists"},
		{name: "custom already exists", build: func() *connect.Error { return AlreadyExistsMsg("duplicate") }, code: connect.CodeAlreadyExists, message: "duplicate"},
		{name: "invalid argument", build: func() *connect.Error { return InvalidArgument("title", "too short") }, code: connect.CodeInvalidArgument, message: "title: too short"},
		{name: "custom invalid argument", build: func() *connect.Error { return InvalidArgumentMsg("bad input") }, code: connect.CodeInvalidArgument, message: "bad input"},
		{name: "required", build: func() *connect.Error { return Required("title") }, code: connect.CodeInvalidArgument, message: "title is required"},
		{name: "invalid entity type", build: func() *connect.Error { return InvalidEntityType("unknown") }, code: connect.CodeInvalidArgument, message: "unsupported entity type: unknown"},
		{name: "invalid entity id", build: InvalidEntityID, code: connect.CodeInvalidArgument, message: MsgInvalidEntityID},
		{name: "invalid sort field", build: func() *connect.Error { return InvalidSortField("created_at") }, code: connect.CodeInvalidArgument, message: "invalid sort field: created_at"},
		{name: "invalid filter field", build: func() *connect.Error { return InvalidFilterField("status") }, code: connect.CodeInvalidArgument, message: "invalid filter field: status"},
		{name: "invalid filter op", build: func() *connect.Error { return InvalidFilterOp("status", "contains") }, code: connect.CodeInvalidArgument, message: "invalid filter operation 'contains' for field 'status'"},
		{name: "failed precondition", build: func() *connect.Error { return FailedPrecondition("not ready") }, code: connect.CodeFailedPrecondition, message: "not ready"},
		{name: "max comment depth", build: func() *connect.Error { return MaxCommentDepth(3) }, code: connect.CodeFailedPrecondition, message: "maximum comment depth of 3 reached"},
		{name: "internal message", build: func() *connect.Error { return InternalMsg("unexpected") }, code: connect.CodeInternal, message: "unexpected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build()
			if got := connect.CodeOf(err); got != tt.code {
				t.Fatalf("code = %v, want %v", got, tt.code)
			}
			if got := err.Message(); got != tt.message {
				t.Fatalf("message = %q, want %q", got, tt.message)
			}
		})
	}
}

func TestCollaborationConflictCarriesStableReasonDetail(t *testing.T) {
	reasons := []intrav1.CollaborationConflictReason{
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
	}
	for _, reason := range reasons {
		err := CollaborationConflict(reason, "reload before saving")
		if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
			t.Fatalf("code = %v, want %v", got, connect.CodeFailedPrecondition)
		}
		if len(err.Details()) != 1 {
			t.Fatalf("details = %d, want 1", len(err.Details()))
		}
		detail, detailErr := err.Details()[0].Value()
		if detailErr != nil {
			t.Fatalf("decode detail: %v", detailErr)
		}
		conflict, ok := detail.(*intrav1.CollaborationConflictDetail)
		if !ok {
			t.Fatalf("detail type = %T", detail)
		}
		if got := conflict.GetReason(); got != reason {
			t.Fatalf("reason = %v, want %v", got, reason)
		}
	}
}

func TestCollaborationMutationRejectionCarriesStableReasonDetail(t *testing.T) {
	reason := intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN
	err := CollaborationMutationRejection(reason, "source room required")

	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want %v", got, connect.CodeInvalidArgument)
	}
	if len(err.Details()) != 1 {
		t.Fatalf("details = %d, want 1", len(err.Details()))
	}
	detail, detailErr := err.Details()[0].Value()
	if detailErr != nil {
		t.Fatalf("decode detail: %v", detailErr)
	}
	rejection, ok := detail.(*intrav1.CollaborationMutationRejectionDetail)
	if !ok {
		t.Fatalf("detail type = %T", detail)
	}
	if got := rejection.GetReason(); got != reason {
		t.Fatalf("reason = %v, want %v", got, reason)
	}
}

func TestInternalWrapsOriginalError(t *testing.T) {
	inner := stderrors.New("database unavailable")

	err := Internal(inner)

	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Fatalf("code = %v, want %v", got, connect.CodeInternal)
	}
	if got := err.Unwrap(); got != inner {
		t.Fatalf("unwrapped error = %v, want %v", got, inner)
	}
}

func TestWrapPreservesConnectErrorAndWrapsPlainErrors(t *testing.T) {
	connectErr := NotFound("post", "post-1")
	if got := Wrap(connectErr); got != connectErr {
		t.Fatal("expected existing connect error to be returned unchanged")
	}

	plainErr := stderrors.New("boom")
	wrapped := Wrap(plainErr)
	if got := connect.CodeOf(wrapped); got != connect.CodeInternal {
		t.Fatalf("code = %v, want %v", got, connect.CodeInternal)
	}
	if got := wrapped.Unwrap(); got != plainErr {
		t.Fatalf("unwrapped error = %v, want %v", got, plainErr)
	}
}
