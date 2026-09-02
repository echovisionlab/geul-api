// Package errors provides standardized Connect RPC error constructors.
package errors

import (
	stderrors "errors"
	"fmt"

	"connectrpc.com/connect"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
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
	MsgSlugAlreadyExists     = "%s with slug '%s' already exists"
	MsgResourceAlreadyExists = "%s with %s '%s' already exists"
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

// Failed precondition messages - Campaign
const (
	MsgCampaignCannotUpdateSent    = "only draft campaigns can be edited"
	MsgCampaignCannotDeleteSending = "cannot delete campaign that is currently sending"
	MsgCampaignOnlyScheduleDraft   = "only draft campaigns can be scheduled"
	MsgCampaignNeedsContent        = "campaign must have content before scheduling"
	MsgCampaignOnlyCancelScheduled = "can only cancel scheduled campaigns"
	MsgCampaignAlreadySent         = "only draft campaigns can be sent"
	MsgCampaignNoRecipients        = "no eligible recipients to send to"
	MsgCampaignSegmentNotFound     = "campaign segment not found"
	MsgCampaignOnlySetSegmentDraft = "only draft campaigns can change audience"
	MsgCampaignOnlySetLayoutDraft  = "only draft campaigns can change layout"
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
func CollaborationConflict(reason intrav1.CollaborationConflictReason, msg string) *connect.Error {
	err := FailedPrecondition(msg)
	detail, detailErr := connect.NewErrorDetail(&intrav1.CollaborationConflictDetail{Reason: reason})
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

// Internal returns a CodeInternal error wrapping the given error.
func Internal(err error) *connect.Error {
	return connect.NewError(connect.CodeInternal, err)
}

// InternalMsg returns a CodeInternal error with a custom message.
func InternalMsg(msg string) *connect.Error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s", msg))
}

// Wrap returns a Connect error with the appropriate code based on the error type.
// If err is already a *connect.Error, it is returned as-is.
// Otherwise, it returns a CodeInternal error.
func Wrap(err error) *connect.Error {
	if connectErr, ok := err.(*connect.Error); ok {
		return connectErr
	}
	return connect.NewError(connect.CodeInternal, err)
}
