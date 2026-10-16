package proxy

import (
	"log/slog"
	"net/http"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

func writeCacheableResponse(
	w http.ResponseWriter,
	r *http.Request,
	data []byte,
	contentType string,
	cacheControl string,
	allowedOrigins []string,
) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Add("Vary", "Origin")
	if origin := r.Header.Get("Origin"); origin != "" && isAllowedOrigin(origin, allowedOrigins) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if _, err := w.Write(data); err != nil {
		slog.ErrorContext(r.Context(), "Failed to write cached delivery response", failurecause.Attr(err), "content_type", contentType)
	}
}
