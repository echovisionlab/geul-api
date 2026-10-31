package errors

import (
	stderrors "errors"

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
}

// Classified returns the Connect form of err when err carries a client
// classification: a Connect error or a use-case error in its chain. Adapters
// that are not Connect handlers (MCP tools) read use-case outcomes through it.
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
		return nil, false
	}
	code, known := domainCodes[domainErr.Kind]
	if !known {
		return nil, false
	}
	client := connect.NewError(code, stderrors.New(domainErr.Message))
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
