package filemedia

import (
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// The upload HTTP handlers and File ingest events answer outside a Connect
// response; these translate use-case errors for them.

// clientErrorText is err's text where a client reads it outside a Connect
// response: an upload HTTP response body, a File ingest failure event, or a
// precondition message that quotes a cause. A use-case error in err reads
// as its Connect form ("invalid_argument: ..."), as it did when these
// outcomes were Connect errors.
func clientErrorText(err error) string {
	text := err.Error()
	var useCase *domainerr.Error
	if !errors.As(err, &useCase) {
		return text
	}
	classified, ok := errs.Classified(useCase)
	if !ok {
		return text
	}
	return strings.Replace(text, useCase.Error(), classified.Error(), 1)
}

func clientArtifactHTTPStatus(err error) int {
	switch errs.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeOutOfRange:
		return http.StatusBadRequest
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeFailedPrecondition, connect.CodeAlreadyExists, connect.CodeAborted:
		return http.StatusConflict
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeCanceled:
		return 499
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

func writeUploadSourceError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch errs.CodeOf(err) {
	case connect.CodeInvalidArgument:
		status = http.StatusBadRequest
	case connect.CodeUnauthenticated:
		status = http.StatusUnauthorized
	case connect.CodePermissionDenied:
		status = http.StatusForbidden
	case connect.CodeNotFound:
		status = http.StatusNotFound
	case connect.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	http.Error(w, http.StatusText(status), status)
}
