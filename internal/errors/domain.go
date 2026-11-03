package errors

import (
	stderrors "errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// domainCodes is the one mapping from use-case outcome kinds to Connect codes.
var domainCodes = map[domainerr.Kind]connect.Code{
	domainerr.Invalid:            connect.CodeInvalidArgument,
	domainerr.NotFound:           connect.CodeNotFound,
	domainerr.AlreadyExists:      connect.CodeAlreadyExists,
	domainerr.FailedPrecondition: connect.CodeFailedPrecondition,
	domainerr.PermissionDenied:   connect.CodePermissionDenied,
	domainerr.Unauthenticated:    connect.CodeUnauthenticated,
	domainerr.Unavailable:        connect.CodeUnavailable,
	domainerr.Aborted:            connect.CodeAborted,
	domainerr.Exhausted:          connect.CodeResourceExhausted,
}

// Classified returns the Connect form of a client outcome carried by err:
// a Connect error, a use-case error, or a malformed UUID database parameter.
// MCP adapters use the same classification as the Connect boundary.
func Classified(err error) (*connect.Error, bool) {
	connectErr, domainErr := outermostClassification(err)
	if connectErr != nil {
		if err == connectErr { //nolint:errorlint // identity: an unwrapped Connect error needs no added context
			return connectErr, true
		}
		// The outer operation context in err survives classification
		// without reaching the client.
		return WithCause(connectErr, err), true
	}
	if domainErr == nil {
		if isRequestUUIDRejection(err) {
			return classifiedError(connect.CodeInvalidArgument, msgInvalidRequestUUID, err), true
		}
		return nil, false
	}
	code, known := domainCodes[domainErr.Kind]
	if !known {
		return nil, false
	}
	client := connect.NewError(code, stderrors.New(domainErr.Message))
	if domainErr.Detail != nil {
		detail, err := connect.NewErrorDetail(domainErr.Detail)
		if err != nil {
			return Internal(fmt.Errorf("encode use-case error detail: %w", err)), true
		}
		client.AddDetail(detail)
	}
	if err == error(domainErr) { //nolint:errorlint // identity: only the use-case error's own cause needs keeping
		if cause := domainErr.Unwrap(); cause != nil {
			return WithCause(client, cause), true
		}
		return client, true
	}
	return WithCause(client, err), true
}

// outermostClassification returns the first Connect or use-case error in
// err's chain. A use case that answers a denial as not found keeps the
// denial as its cause; the outer answer is the one the client sees.
func outermostClassification(err error) (*connect.Error, *domainerr.Error) {
	for err != nil {
		switch classified := err.(type) { //nolint:errorlint // the chain is walked one link at a time
		case *connect.Error:
			return classified, nil
		case *domainerr.Error:
			return nil, classified
		}
		switch wrapped := err.(type) { //nolint:errorlint // the chain is walked one link at a time
		case interface{ Unwrap() error }:
			err = wrapped.Unwrap()
		case interface{ Unwrap() []error }:
			for _, branch := range wrapped.Unwrap() {
				if connectErr, domainErr := outermostClassification(branch); connectErr != nil || domainErr != nil {
					return connectErr, domainErr
				}
			}
			return nil, nil
		default:
			return nil, nil
		}
	}
	return nil, nil
}

// CodeOf is connect.CodeOf for a chain that may carry a use-case error.
// Code that branches on an outcome a use case reports reads it here.
func CodeOf(err error) connect.Code {
	if classified, ok := Classified(err); ok {
		return classified.Code()
	}
	return connect.CodeOf(err)
}
