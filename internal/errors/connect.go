// Package errors provides standardized Connect RPC error constructors.
package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// =============================================================================
// Standard Error Messages - Use these constants for consistent error messages
// =============================================================================

// Authentication messages
const (
	MsgNotAuthenticated       = "not authenticated"
	MsgAuthenticationRequired = "authentication required"
	MsgInvalidSession         = "invalid or expired session"
)

// Authorization messages
const (
	MsgAdminRequired       = "admin access required"
	MsgAuthorRequired      = "author access required"
	MsgAccountBanned       = "account is banned"
	MsgPermissionDenied    = "permission denied"
	MsgNoPermission        = "no permission to %s this %s"
	MsgCannotDeleteSelf    = "cannot delete yourself"
	MsgCannotBanSelf       = "cannot ban yourself"
	MsgCannotDemoteSelf    = "cannot demote yourself"
	MsgInvalidPreviewToken = "invalid preview token"
)

// Resource not found messages
const (
	MsgResourceNotFound = "%s not found: %s"
	MsgUserNotFound     = "user not found"
)

// Already exists messages
const (
	MsgSlugAlreadyExists     = "%s with slug %q already exists"
	MsgResourceAlreadyExists = "%s with %s %q already exists"
	MsgAlreadySubscribed     = "already subscribed"
)

// Invalid argument messages
const (
	MsgFieldRequired      = "%s is required"
	MsgFieldInvalid       = "%s: %s"
	MsgInvalidEntityType  = "unsupported entity type: %s"
	MsgInvalidEntityID    = "invalid entity_id format: must be a valid UUID"
	MsgInvalidSortField   = "invalid sort field: %s"
	MsgInvalidFilterField = "invalid filter field: %s"
	MsgInvalidFilterOp    = "invalid filter operation '%s' for field '%s'"
	MsgExpiresInFuture    = "expires_at must be in the future"
	MsgExpiresMaxOneYear  = "expires_at cannot exceed 1 year from now"
	MsgLimitExceeded      = "limit must be <= %d"
)

// Failed precondition messages - Form
const (
	MsgFormNotAccepting     = "form is not accepting submissions"
	MsgFormNotOpenYet       = "form is not open yet"
	MsgFormClosed           = "form is closed"
	MsgFormAlreadySubmitted = "you have already submitted this form"
)

// Failed precondition messages - Post
const (
	MsgPostAlreadyPublished         = "post is already published"
	MsgPostOnlyPublishedArchive     = "only published posts can be archived"
	MsgPostCannotRemoveLastMgr      = "cannot remove the last manager of a post"
	MsgPostOnlyAdminPublishArchived = "only admin can publish an archived post"
	MsgPostOnlyAdminRevertDraft     = "only admin can revert a post to draft"
)

// Failed precondition messages - Comment
const (
	MsgCommentsDisabled         = "comments are disabled for this post"
	MsgCannotCommentUnpublished = "cannot comment on unpublished post"
	MsgMaxCommentDepth          = "maximum comment depth of %d reached"
	MsgCannotUpdateDeleted      = "cannot update deleted comment"
)

// Failed precondition messages - Privacy/Terms
const (
	MsgCannotUpdateTitle        = "cannot update title of %s %s"
	MsgOnlyScheduleDraft        = "can only schedule draft %s"
	MsgOnlyCancelScheduled      = "can only cancel scheduled %s"
	MsgOnlyActivateDraftOrSched = "can only activate draft or scheduled %s"
	MsgOnlyDeleteDraft          = "can only delete draft %s"
	MsgOnlyPreviewScheduled     = "can only generate preview token for scheduled %s"
	MsgNoEffectiveDate          = "%s has no effective date"
)

// Failed precondition messages - Email Template
const (
	MsgCannotDeleteSystemTemplate = "cannot delete system template"
)

// Failed precondition messages - User
const (
	MsgNoPendingEmailChange = "no pending email change"
)

// Failed precondition messages - File
const (
	MsgMimeNotVerified = "MIME type not verified"
)

// Manager/Member messages
const (
	MsgAlreadyManager = "user is already a manager for this %s"
)

// =============================================================================
// Error Constructors
// =============================================================================

// --- Unauthenticated errors (401) ---

// NotAuthenticated returns a standard "not authenticated" error.
func NotAuthenticated() *connect.Error {
	return connect.NewError(connect.CodeUnauthenticated, stderrors.New(MsgNotAuthenticated))
}

// AuthenticationRequired returns a standard "authentication required" error.
func AuthenticationRequired() *connect.Error {
	return connect.NewError(connect.CodeUnauthenticated, stderrors.New(MsgAuthenticationRequired))
}

// InvalidSession returns a standard "invalid or expired session" error.
func InvalidSession() *connect.Error {
	return connect.NewError(connect.CodeUnauthenticated, stderrors.New(MsgInvalidSession))
}

// DependencyUnavailable distinguishes an unavailable authority/dependency
// from an invalid credential. Authentication middleware must not turn a
// database outage into a 401 that invites a needless login retry.
func DependencyUnavailable(dependency string) *connect.Error {
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s unavailable", dependency))
}

// --- Permission denied errors (403) ---

// PermissionDenied returns a CodePermissionDenied error with the given message.
func PermissionDenied(msg string) *connect.Error {
	return connect.NewError(connect.CodePermissionDenied, stderrors.New(msg))
}

// AdminRequired returns a standard "admin access required" error.
func AdminRequired() *connect.Error {
	return connect.NewError(connect.CodePermissionDenied, stderrors.New(MsgAdminRequired))
}

// AuthorRequired returns a standard "author access required" error.
func AuthorRequired() *connect.Error {
	return connect.NewError(connect.CodePermissionDenied, stderrors.New(MsgAuthorRequired))
}

// AccountBanned returns a standard "account is banned" error.
func AccountBanned() *connect.Error {
	return connect.NewError(connect.CodePermissionDenied, stderrors.New(MsgAccountBanned))
}

// NoPermission returns a "no permission to {action} this {resource}" error.
func NoPermission(action, resource string) *connect.Error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(MsgNoPermission, action, resource))
}

// --- Not found errors (404) ---

// NotFound returns a CodeNotFound error for a resource.
func NotFound(resource, id string) *connect.Error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf(MsgResourceNotFound, resource, id))
}

// NotFoundMsg returns a CodeNotFound error with a custom message.
func NotFoundMsg(msg string) *connect.Error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s", msg))
}

// --- Already exists errors (409) ---

// AlreadyExists returns a CodeAlreadyExists error for a duplicate resource.
func AlreadyExists(resource, field, value string) *connect.Error {
	return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(MsgResourceAlreadyExists, resource, field, value))
}

// SlugAlreadyExists returns a CodeAlreadyExists error for a duplicate slug.
func SlugAlreadyExists(resource, slug string) *connect.Error {
	return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(MsgSlugAlreadyExists, resource, slug))
}

// AlreadyExistsMsg returns a CodeAlreadyExists error with a custom message.
func AlreadyExistsMsg(msg string) *connect.Error {
	return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("%s", msg))
}

// --- Invalid argument errors (400) ---

// InvalidArgument returns a CodeInvalidArgument error.
func InvalidArgument(field, reason string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(MsgFieldInvalid, field, reason))
}

// InvalidArgumentMsg returns a CodeInvalidArgument error with a custom message.
func InvalidArgumentMsg(msg string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", msg))
}

// Required returns a CodeInvalidArgument error for a required field.
func Required(field string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(MsgFieldRequired, field))
}

// InvalidEntityType returns a CodeInvalidArgument error for unsupported entity type.
func InvalidEntityType(entityType string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(MsgInvalidEntityType, entityType))
}

// InvalidEntityID returns a CodeInvalidArgument error for invalid entity ID format.
func InvalidEntityID() *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, stderrors.New(MsgInvalidEntityID))
}

// InvalidSortField returns a CodeInvalidArgument error for invalid sort field.
func InvalidSortField(field string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(MsgInvalidSortField, field))
}

// InvalidFilterField returns a CodeInvalidArgument error for invalid filter field.
func InvalidFilterField(field string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(MsgInvalidFilterField, field))
}

// InvalidFilterOp returns a CodeInvalidArgument error for invalid filter operation.
func InvalidFilterOp(field, op string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(MsgInvalidFilterOp, op, field))
}

// --- Failed precondition errors (400) ---

// FailedPrecondition returns a CodeFailedPrecondition error with the given message.
func FailedPrecondition(msg string) *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", msg))
}

// CollaborationConflict returns a failed-precondition error with a stable
// machine-readable conflict reason. Consumers must branch on the detail rather
// than diagnostic text or conflicting values.
func CollaborationConflict(reason intrav1.CollaborationConflictReason, msg string, causes ...error) *connect.Error {
	err := FailedPrecondition(msg)
	state := &intrav1.CollaborationConflictDetail{Reason: reason}
	for _, cause := range causes {
		var revisions interface {
			CollaborationRevisions() (string, string, *bool)
		}
		if stderrors.As(cause, &revisions) {
			document, target, exists := revisions.CollaborationRevisions()
			if document != "" {
				state.CurrentDocumentRevision = &document
			}
			if target != "" {
				state.CurrentTargetRevision = &target
			}
			state.CurrentTargetExists = exists
		}
	}
	detail, detailErr := connect.NewErrorDetail(state)
	if detailErr != nil {
		return Internal(detailErr)
	}
	err.AddDetail(detail)
	return err
}

// CollaborationMutationRejection returns an invalid-argument error with a
// stable machine-readable rejection reason. This is distinct from a CAS
// conflict because persistence was never attempted.
func CollaborationMutationRejection(
	reason intrav1.CollaborationMutationRejectionReason,
	msg string,
) *connect.Error {
	err := InvalidArgumentMsg(msg)
	detail, detailErr := connect.NewErrorDetail(&intrav1.CollaborationMutationRejectionDetail{Reason: reason})
	if detailErr != nil {
		return Internal(detailErr)
	}
	err.AddDetail(detail)
	return err
}

// MaxCommentDepth returns a CodeFailedPrecondition error for max comment depth exceeded.
func MaxCommentDepth(maxDepth int) *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(MsgMaxCommentDepth, maxDepth))
}

// --- Internal errors (500) ---

// Internal preserves an already-classified error and otherwise returns
// CodeInternal. Callers commonly pass errors from nested domain operations;
// their client-error and dependency classifications must survive wrapping.
func Internal(err error) *connect.Error {
	return Wrap(err)
}

// WithCause returns client with cause kept server-side: the client sees only
// client's code, message and details, while errors.Is/As and the request
// record see cause. Use it where a failure maps to a fixed client answer
// (not found, unavailable, a rejected credential) that would otherwise drop
// the reason.
func WithCause(client *connect.Error, cause error) *connect.Error {
	if cause == nil {
		return client
	}
	// The operation renders the operation context of classified errors in
	// cause in place of their client messages.
	classified := newClassifiedError(client.Code(), &classifiedCause{
		message: client.Message(), operation: failurecause.Message(cause), cause: cause,
	})
	for _, detail := range client.Details() {
		classified.AddDetail(detail)
	}
	for key, values := range client.Meta() {
		classified.Meta()[key] = append([]string(nil), values...)
	}
	return classified
}

// InternalMsg returns a CodeInternal error with a custom message.
func InternalMsg(msg string) *connect.Error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s", msg))
}

// OperationLogLevel grades a failure for a sink whose record has no HTTP
// status to grade it by: the boundary fallback without a request log, the
// MCP tool record, and degraded fallbacks that log and continue. Expected
// client outcomes are Debug, server faults Error, and the remaining client
// rejections Warn.
func OperationLogLevel(err error) slog.Level {
	code := connect.CodeOf(err)
	if classified, ok := Classified(err); ok {
		code = classified.Code()
	}
	switch code {
	case connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeUnauthenticated, connect.CodeCanceled:
		return slog.LevelDebug
	case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss, connect.CodeDeadlineExceeded, connect.CodeUnimplemented:
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// Wrap returns a Connect error with the appropriate code based on the error type.
// If err contains a *connect.Error, its code, message, details and metadata are
// preserved; a use-case error (domainerr) is mapped by its kind. Request cancellation and deadlines retain their transport
// classification. Otherwise, it returns a CodeInternal error.
//
// Wrap does not log: it has no request context. When the classification
// discards outer operation context, the returned error keeps that context for
// the RPC boundary (BoundaryInterceptor), which logs it with the request
// context. Clients only ever see the classified message.
func Wrap(err error) *connect.Error {
	if classified, ok := Classified(err); ok {
		return classified
	}
	if stderrors.Is(err, context.Canceled) {
		return classifiedError(connect.CodeCanceled, context.Canceled.Error(), err)
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		return classifiedError(connect.CodeDeadlineExceeded, context.DeadlineExceeded.Error(), err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

const msgInvalidRequestUUID = "request contains an invalid UUID"

// isRequestUUIDRejection reports whether PostgreSQL rejected a malformed UUID
// bound as a query parameter, which is client input. The same SQLSTATE raised
// while evaluating the query (a stored text value cast to uuid) is data the
// server cannot decode and stays an internal failure. PostgreSQL attributes
// bind-time conversion failures to the parameter in the error context
// ("unnamed portal parameter $1").
func isRequestUUIDRejection(err error) bool {
	var pgErr *pgconn.PgError
	return stderrors.As(err, &pgErr) &&
		pgErr.Code == "22P02" &&
		strings.Contains(pgErr.Message, "invalid input syntax for type uuid") &&
		strings.Contains(pgErr.Where, "parameter $")
}

// classifiedCause is the cause of a Connect error whose client message differs
// from its server-side context. Error returns the client message; Unwrap keeps
// the full chain for errors.Is/As; operation is the loggable server context,
// which failurecause.Message renders through OperationContext.
type classifiedCause struct {
	message   string
	operation string
	rendered  string
	cause     error
}

func (c *classifiedCause) Error() string { return c.message }
func (c *classifiedCause) Unwrap() error { return c.cause }

// OperationContext implements failurecause.OperationContext: the enclosing
// Connect error's text is replaced by the operation context in logs.
func (c *classifiedCause) OperationContext() (rendered, operation string) {
	return c.rendered, c.operation
}

func newClassifiedError(code connect.Code, cause *classifiedCause) *connect.Error {
	classified := connect.NewError(code, cause)
	cause.rendered = classified.Error()
	return classified
}

func classifiedError(code connect.Code, message string, cause error) *connect.Error {
	return newClassifiedError(code, &classifiedCause{message: message, operation: cause.Error(), cause: cause})
}

// CollaborationState preserves revisions read under the owning resource lock.
type CollaborationState struct {
	Document     string
	Target       string
	TargetExists *bool
}

func (s CollaborationState) Error() string { return "collaboration state changed" }
func (s CollaborationState) CollaborationRevisions() (string, string, *bool) {
	return s.Document, s.Target, s.TargetExists
}
