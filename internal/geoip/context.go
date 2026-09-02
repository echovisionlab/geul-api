package geoip

import "context"

type geoipContextKey struct{}

// WithInfo returns a new context with the given GeoIP Info.
func WithInfo(ctx context.Context, info *Info) context.Context {
	return context.WithValue(ctx, geoipContextKey{}, info)
}

// GetInfo extracts the GeoIP Info from the context. Returns nil if not present.
func GetInfo(ctx context.Context) *Info {
	info, _ := ctx.Value(geoipContextKey{}).(*Info)
	return info
}
