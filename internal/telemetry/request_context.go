package telemetry

import (
	"context"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type Actor = sharedtelemetry.Actor
type AnonymousActor = sharedtelemetry.AnonymousActor
type MemberActor = sharedtelemetry.MemberActor
type SystemActor = sharedtelemetry.SystemActor
type RequestContext = sharedtelemetry.RequestContext

const RequestIDHeader = sharedtelemetry.RequestIDHeader

func withRequestContext(ctx context.Context, requestContext RequestContext) context.Context {
	return sharedtelemetry.WithRequestContext(ctx, requestContext)
}

func WithActor(ctx context.Context, actor Actor) context.Context {
	return sharedtelemetry.WithActor(ctx, actor)
}

func RequestContextFrom(ctx context.Context) (RequestContext, bool) {
	return sharedtelemetry.RequestContextFrom(ctx)
}

func RequestIDFromContext(ctx context.Context) string {
	return sharedtelemetry.RequestIDFromContext(ctx)
}

func ContextWithPropagatedRequestID(ctx context.Context, requestID string, actor Actor) context.Context {
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(requestID, actor)
	if err != nil {
		return ctx
	}
	return sharedtelemetry.WithRequestContext(ctx, requestContext)
}
