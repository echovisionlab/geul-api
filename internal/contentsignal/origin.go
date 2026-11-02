package contentsignal

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
)

// ClientOriginHeader carries an opaque browser-tab identity, never an actor or
// authorization claim. Internal collaboration handlers do not install this interceptor.
const ClientOriginHeader = "Geul-Client-Origin"

type clientOriginKey struct{}

// WithClientOrigin validates the boundary value before retaining it in context.
// Empty means the client does not support origin echo suppression.
func WithClientOrigin(ctx context.Context, origin string) (context.Context, error) {
	if origin == "" {
		return ctx, nil
	}
	// Require canonical UUID syntax rather than the URNs and short hex uuid.Parse
	// also accepts. Retain case because origin is opaque.
	parsed, err := uuid.Parse(origin)
	if err != nil || len(origin) != 36 || !strings.EqualFold(parsed.String(), origin) || parsed == uuid.Nil {
		return ctx, errors.New("Geul-Client-Origin must be a nonzero UUID")
	}
	return context.WithValue(ctx, clientOriginKey{}, origin), nil
}

func ClientOrigin(ctx context.Context) string {
	origin, _ := ctx.Value(clientOriginKey{}).(string)
	return origin
}

// ClientOriginInterceptor admits optional origin on session-authenticated RPCs.
func ClientOriginInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			if len(request.Header().Values(ClientOriginHeader)) > 1 {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate Geul-Client-Origin header"))
			}
			ctx, err := WithClientOrigin(ctx, request.Header().Get(ClientOriginHeader))
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, err)
			}
			return next(ctx, request)
		}
	})
}
