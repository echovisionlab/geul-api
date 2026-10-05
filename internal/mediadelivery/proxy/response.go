package proxy

import "net/http"

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
	if origin := r.Header.Get("Origin"); origin != "" && isAllowedOrigin(origin, allowedOrigins) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	_, _ = w.Write(data)
}
