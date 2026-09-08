package proxy

import (
	"fmt"
	"net/http"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

const immutablePublicCacheControl = "public, max-age=31536000, immutable"

func signedCacheControl(claims *mediaauth.Claims) string {
	if claims == nil {
		return immutablePublicCacheControl
	}
	if claims.Purpose == mediaauth.PurposeDownload {
		return "private, no-store"
	}
	remaining := time.Until(time.Unix(claims.ExpiryUnix, 0))
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("private, max-age=%d", int64(remaining/time.Second))
}

func setDeliveryCacheHeaders(w http.ResponseWriter, claims *mediaauth.Claims) {
	w.Header().Set("Cache-Control", signedCacheControl(claims))
	if claims == nil {
		w.Header().Set("CDN-Cache-Control", immutablePublicCacheControl)
		w.Header().Set("Cloudflare-CDN-Cache-Control", immutablePublicCacheControl)
		return
	}
	setNoStoreCDNCacheHeaders(w)
}

func setNoStoreCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	setNoStoreCDNCacheHeaders(w)
}

func setNoStoreCDNCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("CDN-Cache-Control", "no-store")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "no-store")
}
