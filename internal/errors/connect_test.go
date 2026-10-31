package errors

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestWrapKeepsOperationContextOutOfClientMessage(t *testing.T) {
	for _, tc := range []struct {
		inner   error
		message string
	}{
		{connect.NewError(connect.CodeNotFound, stderrors.New("page not found")), "page not found"},
		{context.Canceled, context.Canceled.Error()},
		{context.DeadlineExceeded, context.DeadlineExceeded.Error()},
		{&pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid", Where: "unnamed portal parameter $1"}, "request contains an invalid UUID"},
	} {
		t.Run(tc.inner.Error(), func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			got := Wrap(fmt.Errorf("resolve operation: %w", tc.inner))

			if got.Message() != tc.message {
				t.Fatalf("client message = %q, want %q", got.Message(), tc.message)
			}
			if cause := operationCause(got); !strings.HasPrefix(cause, "resolve operation: ") {
				t.Fatalf("operation context lost: %q", cause)
			}
			if !stderrors.Is(got, tc.inner) {
				t.Fatalf("classified error lost its cause chain: %v", got)
			}
			// Wrap has no request context; the RPC boundary logs instead.
			if output.Len() != 0 {
				t.Fatalf("Wrap logged without request context: %s", output.String())
			}
		})
	}
}

func TestOperationCauseRendersNestedClassifications(t *testing.T) {
	inner := Wrap(fmt.Errorf("load page: %w", NotFoundMsg("page not found")))
	outer := Wrap(fmt.Errorf("render share link: %w", inner))
	if got := operationCause(outer); got != "render share link: load page: not_found: page not found" {
		t.Fatalf("operation cause = %q", got)
	}
	if outer.Message() != "page not found" {
		t.Fatalf("client message = %q", outer.Message())
	}
}

func TestOperationLogLevelKeepsExpectedClientOutcomesOutOfWarnings(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want slog.Level
	}{
		{NotFound("og_generation_target", "id"), slog.LevelDebug},
		{InvalidArgumentMsg("invalid UUID length: 8"), slog.LevelDebug},
		{AuthenticationRequired(), slog.LevelDebug},
		{connect.NewError(connect.CodeCanceled, context.Canceled), slog.LevelDebug},
		{connect.NewError(connect.CodePermissionDenied, stderrors.New("denied")), slog.LevelWarn},
		{connect.NewError(connect.CodeFailedPrecondition, stderrors.New("stale")), slog.LevelWarn},
		{connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded), slog.LevelError},
		{InternalMsg("broken"), slog.LevelError},
		{stderrors.New("unclassified"), slog.LevelError},
	} {
		if got := OperationLogLevel(tc.err); got != tc.want {
			t.Errorf("OperationLogLevel(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}

	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	Wrap(fmt.Errorf("load page: %w", NotFoundMsg("page not found")))
	if output.Len() != 0 {
		t.Fatalf("wrapped not_found logged above Debug: %s", output.String())
	}
}

func TestErrorWrappersPreserveClassifiedErrorsThroughNestedOperations(t *testing.T) {
	for _, wrap := range []struct {
		name string
		fn   func(error) *connect.Error
	}{{"Internal", Internal}, {"Wrap", Wrap}} {
		for code := connect.CodeCanceled; code <= connect.CodeUnauthenticated; code++ {
			t.Run(wrap.name+"/"+code.String(), func(t *testing.T) {
				original := connect.NewError(code, stderrors.New("classified domain outcome"))
				original.Meta().Set("Retry-After", "1")
				detail, err := connect.NewErrorDetail(&intrav1.CollaborationConflictDetail{Reason: intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED})
				if err != nil {
					t.Fatal(err)
				}
				original.AddDetail(detail)
				got := wrap.fn(fmt.Errorf("transaction context: %w", fmt.Errorf("domain context: %w", original)))
				if !stderrors.Is(got, original) || got.Code() != code || got.Message() != original.Message() ||
					len(got.Details()) != 1 || got.Meta().Get("Retry-After") != "1" {
					t.Fatalf("classification/details lost: %v", got)
				}
			})
		}
		for _, tc := range []struct {
			err  error
			code connect.Code
		}{{context.Canceled, connect.CodeCanceled}, {context.DeadlineExceeded, connect.CodeDeadlineExceeded}} {
			got := wrap.fn(fmt.Errorf("database context: %w", tc.err))
			if got.Code() != tc.code || !stderrors.Is(got, tc.err) {
				t.Fatalf("context error misclassified: %v", got)
			}
		}
	}
}

func TestErrorWrappersClassifyOnlyMalformedUUIDDatabaseErrors(t *testing.T) {
	const parameter = "unnamed portal parameter $1"
	for _, tc := range []struct {
		code, message, where string
		want                 connect.Code
	}{
		{"22P02", `invalid input syntax for type uuid: "private-input"`, parameter, connect.CodeInvalidArgument},
		{"22P02", `invalid input syntax for type uuid: "private-input"`, `portal "p1" parameter $2 = '...'`, connect.CodeInvalidArgument},
		// The same rejection while evaluating the query is a stored value the
		// server cannot decode, not client input.
		{"22P02", `invalid input syntax for type uuid: "stored-value"`, "", connect.CodeInternal},
		{"22P02", `invalid input syntax for type uuid: "stored-value"`, "SQL function \"f\" statement 1", connect.CodeInternal},
		{"22P02", `invalid input syntax for type integer: "private-input"`, parameter, connect.CodeInternal},
		{"08006", "database unavailable", "", connect.CodeInternal},
		{"XX000", "invalid input syntax for type uuid", parameter, connect.CodeInternal},
	} {
		driverErr := &pgconn.PgError{Code: tc.code, Message: tc.message, Where: tc.where}
		for _, wrap := range []func(error) *connect.Error{Wrap, Internal} {
			got := wrap(fmt.Errorf("query context: %w", driverErr))
			if got.Code() != tc.want {
				t.Fatalf("%s (%q) misclassified: %v", tc.code, tc.where, got)
			}
			if tc.want == connect.CodeInvalidArgument && got.Message() != "request contains an invalid UUID" {
				t.Fatalf("UUID input leaked: %v", got)
			}
		}
	}
}

// Callers outside the RPC boundary (MCP tools, workers) log classified errors
// with failurecause.Message; the operation context must survive there while
// the client message stays the classified one.
func TestFailureCauseRendersClassifiedOperationContextOutsideConnect(t *testing.T) {
	for _, tc := range []struct {
		name, client, logged string
		err                  *connect.Error
	}{
		{
			name:   "deadline",
			err:    Internal(fmt.Errorf("load campaign: %w", context.DeadlineExceeded)),
			client: context.DeadlineExceeded.Error(),
			logged: "load campaign: context deadline exceeded",
		},
		{
			name:   "classified",
			err:    Wrap(fmt.Errorf("load page: %w", NotFoundMsg("page not found"))),
			client: "page not found",
			logged: "load page: not_found: page not found",
		},
		{
			name:   "nested",
			err:    Internal(fmt.Errorf("render share link: %w", Wrap(fmt.Errorf("load page: %w", NotFoundMsg("page not found"))))),
			client: "page not found",
			logged: "render share link: load page: not_found: page not found",
		},
		{
			name:   "uuid input",
			err:    Wrap(fmt.Errorf("find member: %w", &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid", Where: "unnamed portal parameter $1"})),
			client: "request contains an invalid UUID",
			logged: "find member: : invalid input syntax for type uuid (SQLSTATE 22P02)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Message() != tc.client {
				t.Fatalf("client message = %q, want %q", tc.err.Message(), tc.client)
			}
			if got := failurecause.Message(tc.err); got != tc.logged {
				t.Fatalf("logged cause = %q, want %q", got, tc.logged)
			}
			if got := failurecause.Message(fmt.Errorf("worker: %w", tc.err)); got != "worker: "+tc.logged {
				t.Fatalf("wrapped logged cause = %q", got)
			}
		})
	}
}
