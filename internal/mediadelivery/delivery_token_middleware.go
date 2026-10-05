package mediadelivery

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type originalDeliveryRequest struct {
	url        *url.URL
	requestURI string
}

type originalDeliveryRequestKey struct{}

func redactDeliveryTokenForTelemetry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redactedPath := redactedDeliveryPath(r.URL.Path)
		if redactedPath == r.URL.Path {
			next.ServeHTTP(w, r)
			return
		}
		original := originalDeliveryRequest{url: r.URL, requestURI: r.RequestURI}
		clone := r.Clone(context.WithValue(r.Context(), originalDeliveryRequestKey{}, original))
		clone.URL = cloneURL(r.URL)
		clone.URL.Path = redactedPath
		clone.URL.RawPath = ""
		clone.RequestURI = redactedPath
		if clone.URL.RawQuery != "" {
			clone.RequestURI += "?" + clone.URL.RawQuery
		}
		next.ServeHTTP(w, clone)
	})
}

func restoreDeliveryPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original, ok := r.Context().Value(originalDeliveryRequestKey{}).(originalDeliveryRequest)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL = cloneURL(original.url)
		clone.RequestURI = original.requestURI
		next.ServeHTTP(w, clone)
	})
}

func redactedDeliveryPath(requestPath string) string {
	if !strings.HasPrefix(requestPath, "/media/") {
		return requestPath
	}
	parts := strings.SplitN(strings.TrimPrefix(requestPath, "/media/"), "/", 2)
	if len(parts) != 2 {
		return requestPath
	}
	return "/media/[redacted]/" + parts[1]
}

func cloneURL(source *url.URL) *url.URL {
	clone := *source
	return &clone
}
