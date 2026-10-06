package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestFontResponsesVaryByOriginOnCacheHitsAndMisses(t *testing.T) {
	for _, resource := range []string{"font", "css"} {
		for _, cached := range []bool{false, true} {
			for _, origin := range []string{"", "https://app.example", "https://untrusted.example"} {
				t.Run(resource+"/"+map[bool]string{false: "miss", true: "hit"}[cached]+"/"+origin, func(t *testing.T) {
					cache := &fakeCacheStore{data: []byte("resource"), contentType: "font/woff2"}
					if !cached {
						cache.getErr = io.EOF
					}
					upstreamCalls := 0
					client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						upstreamCalls++
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(http.NoBody)}, nil
					})}
					var handler http.Handler
					path := "/fonts/font.woff2?v=3"
					if resource == "font" {
						proxy := NewFontProxy(fontProxyConfig("http://cache.invalid", "https://fonts.example"), nil)
						proxy.storage, proxy.client, proxy.runBackground = cache, client, runSynchronously
						handler = proxy
					} else {
						proxy := NewFontCSSProxy(fontCSSProxyConfig("http://cache.invalid", "https://fonts.example"), nil)
						proxy.storage, proxy.client, proxy.runBackground = cache, client, runSynchronously
						handler = proxy
						path = "/fonts/css2?family=Inter&v=3"
					}
					req := httptest.NewRequest(http.MethodGet, path, nil)
					req.Header.Set("Origin", origin)
					rec := httptest.NewRecorder()
					rec.Header().Set("Vary", "Accept-Encoding")
					handler.ServeHTTP(rec, req)
					if rec.Code != http.StatusOK {
						t.Fatalf("status = %d", rec.Code)
					}
					vary := rec.Header().Values("Vary")
					if !slices.Contains(vary, "Origin") || !slices.Contains(vary, "Accept-Encoding") {
						t.Fatalf("Vary = %q", vary)
					}
					wantOrigin := ""
					if origin == "https://app.example" {
						wantOrigin = origin
					}
					if got := rec.Header().Get("Access-Control-Allow-Origin"); got != wantOrigin {
						t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, wantOrigin)
					}
					wantCalls := 1
					if cached {
						wantCalls = 0
					}
					if upstreamCalls != wantCalls || cache.putCalls != wantCalls {
						t.Fatalf("upstream calls = %d, cache writes = %d, want %d", upstreamCalls, cache.putCalls, wantCalls)
					}
				})
			}
		}
	}
}
