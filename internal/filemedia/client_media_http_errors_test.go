package filemedia

import (
	"connectrpc.com/connect"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestClientArtifactHTTPStatusPreservesClassifiedFailures(t *testing.T) {
	for _, tc := range []struct {
		code   connect.Code
		status int
	}{
		{connect.CodeInvalidArgument, http.StatusBadRequest},
		{connect.CodeOutOfRange, http.StatusBadRequest},
		{connect.CodeUnauthenticated, http.StatusUnauthorized},
		{connect.CodePermissionDenied, http.StatusForbidden},
		{connect.CodeNotFound, http.StatusNotFound},
		{connect.CodeFailedPrecondition, http.StatusConflict},
		{connect.CodeAlreadyExists, http.StatusConflict},
		{connect.CodeAborted, http.StatusConflict},
		{connect.CodeResourceExhausted, http.StatusTooManyRequests},
		{connect.CodeUnavailable, http.StatusServiceUnavailable},
		{connect.CodeDeadlineExceeded, http.StatusGatewayTimeout},
		{connect.CodeCanceled, 499},
		{connect.CodeUnimplemented, http.StatusNotImplemented},
		{connect.CodeInternal, http.StatusInternalServerError},
		{connect.CodeUnknown, http.StatusInternalServerError},
		{connect.CodeDataLoss, http.StatusInternalServerError},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			err := fmt.Errorf("request failed: %w", connect.NewError(tc.code, errors.New("private dependency details")))
			if got := clientArtifactHTTPStatus(err); got != tc.status {
				t.Fatalf("status=%d want=%d", got, tc.status)
			}
		})
	}
}
