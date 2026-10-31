package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// An inner failure boundary hands the real cause to the request record through
// the context; the record stays the single terminal record for the request.
func TestAccessLogRecordCarriesTheRecordedCauseAndStack(t *testing.T) {
	_, restore := installHTTPTestTelemetry(t)
	defer restore()
	logBuffer, restoreLogger := captureDefaultLogs()
	defer restoreLogger()

	handler := connect.NewUnaryHandler(
		"/test.v1.TestService/GetThing",
		func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			recorder := failurecause.RecorderFrom(ctx)
			require.NotNil(t, recorder, "the request log must install a recorder")
			recorder.Record(failurecause.Failure{
				Cause: "load thing: relation missing",
				Stack: "goroutine 7 [running]:\nmain.handler(...)",
				Attrs: []slog.Attr{slog.String("page_id", "page-1"), slog.String("status_code", "shadowed")},
			})
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		},
		connect.WithInterceptors(NewTracingInterceptor(), NewAccessLogInterceptor(&securityAccessWriterStub{})),
	)
	mux := http.NewServeMux()
	mux.Handle("/test.v1.TestService/", handler)
	request := httptest.NewRequest(http.MethodPost, "/test.v1.TestService/GetThing", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	NewHTTPHandler(mux, nil).ServeHTTP(httptest.NewRecorder(), request)

	entry := decodeSingleLogEntry(t, logBuffer.Bytes())
	require.Equal(t, "request.completed", entry["event"])
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, "load thing: relation missing", entry["failure_cause"])
	require.Equal(t, "goroutine 7 [running]:\nmain.handler(...)", entry["panic_stack"])
	require.Equal(t, "page-1", entry["page_id"])
	require.EqualValues(t, http.StatusInternalServerError, entry["status_code"], "an identifier must not replace a request record field")
}

// A plain HTTP handler records its cause the same way; the HTTP request record
// stays the only record of the failure.
func TestHTTPRequestRecordCarriesTheRecordedCause(t *testing.T) {
	_, restore := installHTTPTestTelemetry(t)
	defer restore()
	logBuffer, restoreLogger := captureDefaultLogs()
	defer restoreLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /uploads/{id}", func(w http.ResponseWriter, r *http.Request) {
		failurecause.RecorderFrom(r.Context()).Record(failurecause.Of(failurecause.WithAttrs(
			errors.New("store artifact: bucket missing"), slog.String("file_id", "file-1"),
		)))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	})
	request := httptest.NewRequest(http.MethodPost, "/uploads/file-1", strings.NewReader("{}"))
	NewHTTPHandler(mux, nil).ServeHTTP(httptest.NewRecorder(), request)

	entry := decodeSingleLogEntry(t, logBuffer.Bytes())
	require.Equal(t, "request.completed", entry["event"])
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, "store artifact: bucket missing", entry["failure_cause"])
	require.Equal(t, "file-1", entry["file_id"])
}
