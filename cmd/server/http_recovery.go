package main

import (
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"

	"connectrpc.com/connect"
	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/telemetry"
)

// connectInterceptors orders the interceptors shared by every Connect route:
// tracing and the request log outermost, then the failure boundary, then the
// route-specific interceptors. The boundary sits inside the request log so
// request.completed records only what the client receives, and it recovers
// panics raised by the inner interceptors as well as by handlers.
func connectInterceptors(tracing, accessLog connect.Interceptor, inner ...connect.Interceptor) connect.HandlerOption {
	interceptors := append([]connect.Interceptor{tracing, accessLog, errs.NewBoundaryInterceptor()}, inner...)
	return connect.WithInterceptors(interceptors...)
}

// recoverHTTPPanics turns a plain HTTP handler panic into a 500 response and
// an Error log with the stack. Without it net/http logs the panic on its own
// logger and drops the connection. Connect routes already recover in their
// interceptor chain. A panic after the response started cannot change the
// status, so the connection is aborted to keep the response visibly truncated.
func recoverHTTPPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var started atomic.Bool
		tracked := httpsnoop.Wrap(w, httpsnoop.Hooks{
			WriteHeader: func(write httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
				return func(code int) {
					started.Store(true)
					write(code)
				}
			},
			Write: func(write httpsnoop.WriteFunc) httpsnoop.WriteFunc {
				return func(body []byte) (int, error) {
					started.Store(true)
					return write(body)
				}
			},
			ReadFrom: func(readFrom httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
				return func(source io.Reader) (int64, error) {
					started.Store(true)
					return readFrom(source)
				}
			},
			Flush: func(flush httpsnoop.FlushFunc) httpsnoop.FlushFunc {
				return func() {
					started.Store(true)
					flush()
				}
			},
		})
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, compared like net/http
				panic(recovered)
			}
			cause, stack := failurecause.Panic(recovered)
			// The ingress span ended while the panic unwound; the propagated
			// trace and the request ID header still correlate this record.
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			slog.ErrorContext(ctx, "HTTP handler panicked",
				"http_method", r.Method,
				"request_id", w.Header().Get(telemetry.RequestIDHeader),
				"failure_cause", cause,
				"panic_stack", stack,
			)
			if started.Load() {
				panic(http.ErrAbortHandler)
			}
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}()
		next.ServeHTTP(tracked, r)
	})
}
