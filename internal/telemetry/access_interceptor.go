package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// AccessLogInterceptor emits the one terminal request.completed record for a
// Connect request. It deliberately does not infer Domain Audit from RPC names.
type AccessLogInterceptor struct {
	securityWriter SecurityAccessAppender
}

func NewAccessLogInterceptor(securityWriter SecurityAccessAppender) *AccessLogInterceptor {
	if securityWriter == nil {
		panic("access log interceptor security writer is required")
	}
	return &AccessLogInterceptor{securityWriter: securityWriter}
}

func (i *AccessLogInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		service, method := splitProcedure(req.Spec().Procedure)
		i.appendAuthorizationDenial(ctx, req.Spec().Procedure, err)
		emitConnectRequestRecord(ctx, req.HTTPMethod(), service, method, start, err)
		markConnectRequestRecorded(ctx)
		return resp, err
	}
}

func (i *AccessLogInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *AccessLogInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		service, method := splitProcedure(conn.Spec().Procedure)
		i.appendAuthorizationDenial(ctx, conn.Spec().Procedure, err)
		emitConnectRequestRecord(ctx, "", service, method, start, err)
		markConnectRequestRecorded(ctx)
		return err
	}
}

func (i *AccessLogInterceptor) appendAuthorizationDenial(ctx context.Context, procedure string, terminalErr error) {
	var reason sharedtelemetry.AuthorizationDenialReason
	switch connect.CodeOf(terminalErr) {
	case connect.CodeUnauthenticated:
		reason = sharedtelemetry.AuthorizationDeniedAuthenticationRequired
	case connect.CodePermissionDenied:
		reason = sharedtelemetry.AuthorizationDeniedPermissionDenied
	default:
		return
	}

	actor, err := requestRecordActor(ctx)
	if err != nil {
		ReportSecurityAccessAppendFailure(ctx, sharedtelemetry.SecurityAuthorizationDenied, sharedtelemetry.AuditAppendFailureActorInvalid)
		return
	}
	if actor.Kind == sharedtelemetry.ActorKindSystem {
		return
	}
	_ = BuildAndAppendSecurityAccess(
		ctx,
		i.securityWriter,
		sharedtelemetry.SecurityAuthorizationDenied,
		actor,
		time.Now().UTC(),
		func(metadata sharedtelemetry.SecurityAccessMetadata) (sharedtelemetry.SecurityAccessRecord, error) {
			return sharedtelemetry.NewAuthorizationDeniedRecord(metadata, procedure, reason)
		},
	)
}

func requestRecordActor(ctx context.Context) (sharedtelemetry.RecordActor, error) {
	requestContext, ok := RequestContextFrom(ctx)
	if !ok {
		return sharedtelemetry.ActorForRecord(sharedtelemetry.AnonymousActor{})
	}
	return sharedtelemetry.ActorForRecord(requestContext.Actor)
}

func requestActorAndCorrelation(ctx context.Context) (sharedtelemetry.RecordActor, sharedtelemetry.Correlation) {
	recordActor, err := requestRecordActor(ctx)
	if err != nil {
		recordActor, _ = sharedtelemetry.ActorForRecord(sharedtelemetry.AnonymousActor{})
	}
	return recordActor, sharedtelemetry.CorrelationFromContext(ctx)
}

func slogDefaultHandler() slog.Handler {
	return slog.Default().Handler()
}

func emitConnectRequestRecord(ctx context.Context, httpMethod, service, method string, start time.Time, err error) {
	actor, correlation := requestActorAndCorrelation(ctx)
	result := sharedtelemetry.RequestResult{
		StatusCode: connectHTTPStatus(err),
		DurationMS: time.Since(start).Milliseconds(),
		Outcome:    connectOutcome(err),
	}
	if err != nil {
		result.ErrorCode = connect.CodeOf(err).String()
	}
	record, buildErr := sharedtelemetry.NewRPCRequestRecord(sharedtelemetry.RequestMetadata{
		OccurredAt: time.Now().UTC(), Correlation: correlation, RecordActor: actor,
	}, httpMethod, service, method, result)
	if buildErr != nil {
		return
	}
	_ = sharedtelemetry.EmitRequest(ctx, slogDefaultHandler(), record)
}

func splitProcedure(procedure string) (service, method string) {
	procedure = strings.TrimPrefix(procedure, "/")
	parts := strings.SplitN(procedure, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func connectOutcome(err error) sharedtelemetry.RequestOutcome {
	if err == nil {
		return sharedtelemetry.RequestOutcomeSucceeded
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeResourceExhausted:
		return sharedtelemetry.RequestOutcomeBlocked
	default:
		return sharedtelemetry.RequestOutcomeFailed
	}
}

func connectHTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch connect.CodeOf(err) {
	case connect.CodeCanceled:
		return 499
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeOutOfRange:
		return http.StatusBadRequest
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted:
		return http.StatusConflict
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}
