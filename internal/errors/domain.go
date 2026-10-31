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
	var connectErr *connect.Error
	if stderrors.As(err, &connectErr) && connectErr != nil {
		if err == connectErr { //nolint:errorlint // identity: an unwrapped Connect error needs no added context
			return connectErr, true
		}
		// The outer operation context in err survives classification
		// without reaching the client.
		return WithCause(connectErr, err), true
	}
	domainErr, ok := stderrors.AsType[*domainerr.Error](err)
	if !ok || domainErr == nil {
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

