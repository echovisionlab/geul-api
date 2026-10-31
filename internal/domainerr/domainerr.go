// Package domainerr is the error type of use cases. A use case reports an
// outcome the client can act on as an *Error: a kind, the request field it
// concerns, and a client-safe message. It never names a transport; Connect
// handlers and MCP tools map it at their boundary through internal/errors.
package domainerr

import (
	"fmt"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"google.golang.org/protobuf/proto"
)

// Kind classifies an outcome by what the client can do about it.
type Kind uint8

const (
	// Invalid rejects the request as written.
	Invalid Kind = iota + 1
	// NotFound reports a missing resource.
	NotFound
	// AlreadyExists reports a uniqueness conflict.
	AlreadyExists
	// FailedPrecondition rejects a request the current state does not allow.
	FailedPrecondition
	// PermissionDenied rejects an authenticated caller.
	PermissionDenied
	// Unauthenticated rejects a missing or stale session.
	Unauthenticated
	// Unavailable reports a dependency that may recover on retry.
	Unavailable
)

// Error is a use-case outcome. Message is what the client sees; the cause
// stays server-side and reaches the failure record.
type Error struct {
	Kind    Kind
	Field   string
	Message string
	// Detail is a machine-readable reason the client branches on, such as
	// a collaboration conflict.
	Detail proto.Message
	cause  error
}

func (e *Error) Error() string {
	if e.cause == nil {
		return e.Message
	}
	return e.Message + ": " + e.cause.Error()
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause returns a copy of e that keeps cause behind the client message.
func (e *Error) WithCause(cause error) *Error {
	copied := *e
	copied.cause = cause
	return &copied
}

// New returns an error of kind about field with message.
func New(kind Kind, field, message string) *Error {
	return &Error{Kind: kind, Field: field, Message: message}
}

// InvalidField rejects field for reason.
func InvalidField(field, reason string) *Error {
	return New(Invalid, field, fmt.Sprintf("%s: %s", field, reason))
}

// Required rejects a missing field.
func Required(field string) *Error {
	return New(Invalid, field, field+" is required")
}

// ResourceNotFound reports that resource id does not exist.
func ResourceNotFound(resource, id string) *Error {
	return New(NotFound, "", fmt.Sprintf("%s not found: %s", resource, id))
}

// Duplicate reports that another resource already holds value in field.
func Duplicate(resource, field, value string) *Error {
	return New(AlreadyExists, field, fmt.Sprintf("%s with %s '%s' already exists", resource, field, value))
}

// Precondition rejects a request the current state does not allow.
func Precondition(message string) *Error {
	return New(FailedPrecondition, "", message)
}

// Denied rejects an authenticated caller.
func Denied(message string) *Error {
	return New(PermissionDenied, "", message)
}

// InvalidSession rejects a session whose principal no longer matches the
// stored Member and Identity.
func InvalidSession() *Error {
	return New(Unauthenticated, "", "invalid or expired session")
}

// DependencyUnavailable reports an unavailable dependency.
func DependencyUnavailable(dependency string) *Error {
	return New(Unavailable, "", dependency+" unavailable")
}

// AuthenticationRequired rejects a request without an authenticated
// principal.
func AuthenticationRequired() *Error {
	return New(Unauthenticated, "", "authentication required")
}

// AccountBanned rejects a principal whose account is banned.
func AccountBanned() *Error {
	return New(PermissionDenied, "", "account is banned")
}

// NoPermission rejects a caller without action on resource.
func NoPermission(action, resource string) *Error {
	return New(PermissionDenied, "", fmt.Sprintf("no permission to %s this %s", action, resource))
}

// AdminRequired rejects a caller without administrator authority.
func AdminRequired() *Error {
	return New(PermissionDenied, "", "admin access required")
}

// AuthorRequired rejects a caller without author authority.
func AuthorRequired() *Error {
	return New(PermissionDenied, "", "author access required")
}

// InvalidEntityType rejects an unsupported entity type.
func InvalidEntityType(entityType string) *Error {
	return New(Invalid, "entity_type", "unsupported entity type: "+entityType)
}

// InvalidEntityID rejects an entity ID that is not a UUID.
func InvalidEntityID() *Error {
	return New(Invalid, "entity_id", "invalid entity_id format: must be a valid UUID")
}

// CollaborationRevisions are the revisions a collaboration save found under
// the owning resource lock.
type CollaborationRevisions struct {
	Document     string
	Target       string
	TargetExists *bool
}

// CollaborationConflict rejects a collaboration save whose expected
// revisions changed. Consumers branch on the reason detail rather than the
// message.
func CollaborationConflict(reason intrav1.CollaborationConflictReason, message string, current CollaborationRevisions) *Error {
	detail := &intrav1.CollaborationConflictDetail{Reason: reason, CurrentTargetExists: current.TargetExists}
	if current.Document != "" {
		detail.CurrentDocumentRevision = &current.Document
	}
	if current.Target != "" {
		detail.CurrentTargetRevision = &current.Target
	}
	conflict := Precondition(message)
	conflict.Detail = detail
	return conflict
}

// CollaborationRejection rejects a collaboration save the room may not make;
// persistence was never attempted.
func CollaborationRejection(reason intrav1.CollaborationMutationRejectionReason, message string) *Error {
	return &Error{Kind: Invalid, Message: message, Detail: &intrav1.CollaborationMutationRejectionDetail{Reason: reason}}
}
