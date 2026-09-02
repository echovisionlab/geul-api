package telemetry

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/requestip"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var httpTracer = otel.Tracer(sharedtelemetry.ServiceBackend.Instrumentation("http"))

type connectRecordStateKey struct{}

type connectRecordState struct {
	mu       sync.Mutex
	recorded bool
}

func withConnectRecordState(ctx context.Context) context.Context {
	return context.WithValue(ctx, connectRecordStateKey{}, &connectRecordState{})
}

func markConnectRequestRecorded(ctx context.Context) {
	state, ok := ctx.Value(connectRecordStateKey{}).(*connectRecordState)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.recorded = true
}

func connectRequestRecorded(ctx context.Context) bool {
	state, ok := ctx.Value(connectRecordStateKey{}).(*connectRecordState)
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.recorded
}

// NewHTTPHandler creates the API ingress boundary. It extracts W3C context,
// creates a server span without raw URL/query attributes, replaces untrusted
// request IDs, and emits a terminal request record for non-Connect handlers.
// trustIncomingRequestID must return true only after authenticating a trusted
// Geul caller; nil treats every incoming request ID as untrusted.
func NewHTTPHandler(mux *http.ServeMux, trustIncomingRequestID func(*http.Request) bool) http.Handler {
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, pattern := mux.Handler(r)
		r.Pattern = pattern
		handler.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestContext := ingressRequestContext(r, trustIncomingRequestID)
		requestID := requestContext.RequestID
		ctx := withRequestContext(r.Context(), requestContext)
		ctx = withConnectRecordState(ctx)
		w.Header().Set(RequestIDHeader, requestID)
		if isHealthPath(r.URL.Path) {
			dispatch.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
		ctx, span := httpTracer.Start(ctx, "http.server",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("request_id", requestID),
				attribute.String("http.request.method", r.Method),
			),
		)
		defer span.End()

		request := r.WithContext(ctx)
		metrics := httpsnoop.CaptureMetrics(dispatch, w, request)
		route := normalizeHTTPRoute(request.Pattern, r.Method)
		if route == "" {
			route = "unmatched"
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", metrics.Code),
		)
		if metrics.Code >= http.StatusBadRequest {
			span.SetStatus(codes.Error, "")
		}
		if !connectRequestRecorded(ctx) {
			logHTTPRequest(ctx, r.Method, route, metrics.Code, metrics.Duration)
		}
	})
}

func ingressRequestContext(r *http.Request, trustIncomingRequestID func(*http.Request) bool) RequestContext {
	if trustIncomingRequestID != nil && trustIncomingRequestID(r) {
		requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
			r.Header.Get(RequestIDHeader),
			sharedtelemetry.AnonymousActor{},
		)
		if err == nil {
			return requestContext
		}
	}
	requestContext, err := sharedtelemetry.NewPublicRequestContext(trustedSourceIP(r))
	if err != nil {
		requestContext, _ = sharedtelemetry.NewPublicRequestContext("")
	}
	return requestContext
}

func normalizeHTTPRoute(pattern, method string) string {
	return strings.TrimPrefix(pattern, method+" ")
}

func isHealthPath(path string) bool {
	return path == "/health" || strings.HasPrefix(path, "/health/")
}

func trustedSourceIP(request *http.Request) string {
	candidate := requestip.TrustedClientIP(
		request.Header.Get("X-Forwarded-For"),
		request.Header.Get("X-Real-IP"),
		request.RemoteAddr,
	)
	address, err := netip.ParseAddr(candidate)
	if err != nil {
		return ""
	}
	return address.Unmap().String()
}

func logHTTPRequest(ctx context.Context, method, route string, statusCode int, duration time.Duration) {
	actor, correlation := requestActorAndCorrelation(ctx)
	result, err := sharedtelemetry.ClassifyHTTPResult(statusCode, duration.Milliseconds())
	if err != nil {
		return
	}
	record, err := sharedtelemetry.NewHTTPRequestRecord(sharedtelemetry.RequestMetadata{
		OccurredAt: time.Now().UTC(), Correlation: correlation, RecordActor: actor,
	}, method, route, result)
	if err != nil {
		return
	}
	_ = sharedtelemetry.EmitRequest(ctx, slogDefaultHandler(), record)
}
