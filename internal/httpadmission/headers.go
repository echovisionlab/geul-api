// Package httpadmission defines transport headers shared by HTTP admission owners.
package httpadmission

import (
	"net/http"
	"strings"
)

const (
	Challenge       = "WWW-Authenticate"
	RetryAfter      = "Retry-After"
	RateLimit       = "RateLimit"
	RateLimitPolicy = "RateLimit-Policy"
)

// Copy preserves admission metadata without changing protocol-specific bodies.
func Copy(dst, src http.Header) {
	for _, name := range []string{Challenge, RetryAfter, RateLimit, RateLimitPolicy} {
		for _, value := range src.Values(name) {
			if value = strings.TrimSpace(value); value != "" {
				dst.Add(name, value)
			}
		}
	}
}
