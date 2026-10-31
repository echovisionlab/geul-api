package telemetry

import (
	"context"
	"errors"
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
			recorder.Record("load thing: relation missing", "goroutine 7 [running]:\nmain.handler(...)")
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
}
