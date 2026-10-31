package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type boundaryContextKey struct{}

type boundaryLogRecord struct {
	level   slog.Level
	message string
	attrs   map[string]string
	request any
}

type boundaryLogCapture struct {
	mu      sync.Mutex
	records []boundaryLogRecord
}

func (c *boundaryLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *boundaryLogCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *boundaryLogCapture) WithGroup(string) slog.Handler            { return c }

func (c *boundaryLogCapture) Handle(ctx context.Context, record slog.Record) error {
	attrs := map[string]string{}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.String()
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, boundaryLogRecord{
		level: record.Level, message: record.Message, attrs: attrs, request: ctx.Value(boundaryContextKey{}),
	})
	return nil
}

func (c *boundaryLogCapture) only(t *testing.T) boundaryLogRecord {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records) != 1 {
		t.Fatalf("log records = %+v, want exactly one", c.records)
	}
	return c.records[0]
}

func captureBoundaryLogs(t *testing.T) *boundaryLogCapture {
	t.Helper()
	capture := &boundaryLogCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return capture
}

const boundaryPrivateToken = "share-token-private-value"

func callBoundaryUnary(t *testing.T, handler connect.UnaryFunc) (context.Context, error) {
	t.Helper()
	ctx := context.WithValue(t.Context(), boundaryContextKey{}, "request-scope")
	request := connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: boundaryPrivateToken})
	_, err := NewBoundaryInterceptor().WrapUnary(handler)(ctx, request)
	return ctx, err
}

func requireOpaqueInternal(t *testing.T, err error) {
	t.Helper()
	var connectErr *connect.Error
	if !stderrors.As(err, &connectErr) {
		t.Fatalf("error = %T, want *connect.Error", err)
	}
	if connectErr.Code() != connect.CodeInternal || connectErr.Message() != MsgInternal {
		t.Fatalf("client error = %v, want opaque internal", connectErr)
	}
	if len(connectErr.Details()) != 0 || connectErr.Unwrap().Error() != MsgInternal {
		t.Fatalf("client error carries its cause: %v", connectErr.Unwrap())
	}
}

func TestBoundaryMakesInternalErrorsOpaqueAndLogsCauseWithRequestContext(t *testing.T) {
	cause := fmt.Errorf("load subscription for %s: %w", boundaryPrivateToken,
		stderrors.New(`ERROR: relation "newsletter" does not exist (SQLSTATE 42P01)`))
	for name, returned := range map[string]error{
		"Internal":     Internal(cause),
		"InternalMsg":  InternalMsg("kratos responded: " + boundaryPrivateToken),
		"plain":        cause,
		"wrapped once": fmt.Errorf("handler: %w", Internal(cause)),
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureBoundaryLogs(t)
			_, err := callBoundaryUnary(t, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				return nil, returned
			})

			requireOpaqueInternal(t, err)
			record := logs.only(t)
			if record.level != slog.LevelError || record.message != "RPC failed" {
				t.Fatalf("log = %+v, want RPC failed at Error", record)
			}
			if record.request != "request-scope" {
				t.Fatal("failure was not logged with the request context")
			}
			if record.attrs["error_code"] != "internal" && record.attrs["error_code"] != "unknown" {
				t.Fatalf("error_code = %q", record.attrs["error_code"])
			}
			logged := record.attrs["failure_cause"]
			if strings.Contains(logged, boundaryPrivateToken) {
				t.Fatalf("logged cause exposed a private request value: %s", logged)
			}
			if name != "InternalMsg" && !strings.Contains(logged, "relation \"newsletter\" does not exist") {
				t.Fatalf("logged cause lost the server-side detail: %s", logged)
			}
		})
	}
}

func TestBoundaryRecoversHandlerPanicAsInternalWithStack(t *testing.T) {
	logs := captureBoundaryLogs(t)
	_, err := callBoundaryUnary(t, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		panic("nil map write while handling " + boundaryPrivateToken)
	})

	requireOpaqueInternal(t, err)
	record := logs.only(t)
	if record.level != slog.LevelError || record.message != "RPC handler panicked" || record.request != "request-scope" {
		t.Fatalf("log = %+v, want panic at Error with request context", record)
	}
	if !strings.Contains(record.attrs["panic_stack"], "TestBoundaryRecoversHandlerPanicAsInternalWithStack") {
		t.Fatalf("panic stack missing the panicking frame: %s", record.attrs["panic_stack"])
	}
	if cause := record.attrs["failure_cause"]; !strings.Contains(cause, "nil map write") || strings.Contains(cause, boundaryPrivateToken) {
		t.Fatalf("panic cause = %q", cause)
	}
}

func TestBoundaryKeepsAbortHandlerPanicSemantics(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler { //nolint:errorlint // sentinel panic value
			t.Fatalf("recovered = %v, want http.ErrAbortHandler", recovered)
		}
	}()
	_, _ = callBoundaryUnary(t, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		panic(http.ErrAbortHandler)
	})
}

type boundaryStreamConn struct {
	connect.StreamingHandlerConn
}

func (boundaryStreamConn) Spec() connect.Spec {
	return connect.Spec{Procedure: "/geul.test.v1.TestService/Stream", StreamType: connect.StreamTypeServer}
}

func TestBoundaryRecoversStreamingHandlerPanic(t *testing.T) {
	logs := captureBoundaryLogs(t)
	ctx := context.WithValue(t.Context(), boundaryContextKey{}, "request-scope")
	err := NewBoundaryInterceptor().WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		panic("stream broke")
	})(ctx, boundaryStreamConn{})

	requireOpaqueInternal(t, err)
	if record := logs.only(t); record.message != "RPC handler panicked" || record.request != "request-scope" ||
		record.attrs["rpc_procedure"] != "/geul.test.v1.TestService/Stream" {
		t.Fatalf("log = %+v", record)
	}
}

func TestBoundaryLogsClassifiedOperationContextWithRequestContext(t *testing.T) {
	logs := captureBoundaryLogs(t)
	_, err := callBoundaryUnary(t, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, Wrap(fmt.Errorf("resolve share link %s: %w", boundaryPrivateToken, NotFoundMsg("page not found")))
	})

	if connect.CodeOf(err) != connect.CodeNotFound || err.(*connect.Error).Message() != "page not found" {
		t.Fatalf("client error = %v", err)
	}
	record := logs.only(t)
	if record.level != slog.LevelDebug || record.message != "Classified RPC error" || record.request != "request-scope" {
		t.Fatalf("log = %+v, want classified context at Debug with request context", record)
	}
	if cause := record.attrs["failure_cause"]; cause != "resolve share link [redacted]: not_found: page not found" {
		t.Fatalf("failure_cause = %q", cause)
	}
}

func TestBoundaryRedactsPrivateRequestValuesFromClientMessages(t *testing.T) {
	captureBoundaryLogs(t)
	_, err := callBoundaryUnary(t, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, NotFoundMsg("share link " + boundaryPrivateToken + " not found")
	})

	// request.completed records this message; it must not carry the token.
	if got := err.(*connect.Error).Message(); got != "share link [redacted] not found" || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("client error = %v", err)
	}
}

func TestBoundaryPassesUnchangedClientErrorsWithoutLogging(t *testing.T) {
	logs := captureBoundaryLogs(t)
	original := InvalidArgument("title", "too short")
	_, err := callBoundaryUnary(t, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, original
	})
	if err != original { //nolint:errorlint // identity is the property under test
		t.Fatalf("client error = %v, want the handler's error unchanged", err)
	}
	if len(logs.records) != 0 {
		t.Fatalf("unexpected logs: %+v", logs.records)
	}
}

func TestRequestPrivateValuesSelectsSensitiveFieldsOnly(t *testing.T) {
	for name, want := range map[string]bool{
		"token": true, "share_token": true, "share_password": true, "email": true, "new_email": true,
		"code": true, "verification_code": true, "api_key": true, "recipient": true,
		"country_code": false, "object_key": false, "slug": false, "title": false, "member_id": false,
	} {
		if got := isPrivateField(protoreflect.Name(name)); got != want {
			t.Errorf("isPrivateField(%q) = %v, want %v", name, got, want)
		}
	}
	values := requestPrivateValues(&openv1.UnsubscribeNewsletterRequest{Token: boundaryPrivateToken})
	if len(values) != 1 || values[0] != boundaryPrivateToken {
		t.Fatalf("private values = %v", values)
	}
}
