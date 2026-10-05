package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
)

func TestFontCSSProxyRewritesAndCachesFontURLs(t *testing.T) {
	t.Parallel()

	s3 := newProxyFakeMinioServer(t, map[string]proxyFakeS3Object{})
	defer s3.Close()

	var gotUA string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		if r.URL.RawQuery != "family=Inter&display=swap" {
			t.Fatalf("upstream query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`@font-face{src:url(https://fonts.gstatic.com/s/inter/font.woff2)}`))
	}))
	defer upstream.Close()

	cfg := fontCSSProxyConfig(s3.URL, upstream.URL)
	minioClient, err := storage.NewMinioClient(cfg)
	if err != nil {
		t.Fatalf("NewMinioClient returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/fonts/css2?family=Inter&display=swap", nil)
	req.Header.Set("User-Agent", "legacy-client")
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()

	proxy := NewFontCSSProxy(cfg, minioClient)
	cache := &fakeCacheStore{getErr: io.EOF}
	proxy.storage = cache
	proxy.runBackground = runSynchronously
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "https://cdn.example/fonts/s/inter/font.woff2") {
		t.Fatalf("rewritten CSS = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if gotUA != googleFontsWOFF2UserAgent {
		t.Fatalf("upstream user-agent = %q", gotUA)
	}
	if cache.putCalls != 1 {
		t.Fatalf("cache put calls = %d", cache.putCalls)
	}
}

func TestFontCSSProxyUsesDeterministicCacheAcrossClientUserAgents(t *testing.T) {
	t.Parallel()

	var upstreamCalls int
	var upstreamUserAgents []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		upstreamUserAgents = append(upstreamUserAgents, r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(`@font-face{src:url(https://fonts.gstatic.com/s/inter/font.woff2) format('woff2')}`))
	}))
	defer upstream.Close()

	cache := &fontCSSMemoryCache{objects: make(map[string][]byte)}
	proxy := NewFontCSSProxy(fontCSSProxyConfig("http://cache.invalid", upstream.URL), nil)
	proxy.storage = cache
	proxy.runBackground = runSynchronously

	for _, clientUA := range []string{"legacy-client", "current-client"} {
		req := httptest.NewRequest(http.MethodGet, "/fonts/css2?family=Inter&display=swap", nil)
		req.Header.Set("User-Agent", clientUA)
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("client %q status = %d", clientUA, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "https://cdn.example/fonts/s/inter/font.woff2") {
			t.Fatalf("client %q CSS = %q", clientUA, rec.Body.String())
		}
	}

	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
	if len(upstreamUserAgents) != 1 || upstreamUserAgents[0] != googleFontsWOFF2UserAgent {
		t.Fatalf("upstream user-agents = %q", upstreamUserAgents)
	}
	if len(cache.getKeys) != 2 || cache.getKeys[0] != cache.getKeys[1] {
		t.Fatalf("cache get keys = %q", cache.getKeys)
	}
	wantPrefix := "font-css/" + fontCSSCacheVersion + "/"
	if !strings.HasPrefix(cache.getKeys[0], wantPrefix) {
		t.Fatalf("cache key = %q, want prefix %q", cache.getKeys[0], wantPrefix)
	}
	if len(cache.putKeys) != 1 || cache.putKeys[0] != cache.getKeys[0] {
		t.Fatalf("cache put keys = %q, get keys = %q", cache.putKeys, cache.getKeys)
	}
}

func TestFontCSSProxyFailureModes(t *testing.T) {
	s3 := newProxyFakeMinioServer(t, map[string]proxyFakeS3Object{})
	defer s3.Close()
	cfg := fontCSSProxyConfig(s3.URL, "https://fonts.example")
	minioClient, err := storage.NewMinioClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		upstream  string
		transport http.RoundTripper
		want      int
	}{
		{"invalid URL", "http://[::1", nil, http.StatusInternalServerError},
		{"transport error", "https://fonts.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		}), http.StatusBadGateway},
		{"upstream error", "https://fonts.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("slow down"))}, nil
		}), http.StatusTooManyRequests},
		{"read error", "https://fonts.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &errorReadCloser{err: io.ErrUnexpectedEOF}}, nil
		}), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caseCfg := *cfg
			caseCfg.FontCSSUpstream = tt.upstream
			proxy := NewFontCSSProxy(&caseCfg, minioClient)
			if tt.transport != nil {
				proxy.client = &http.Client{Transport: tt.transport}
			}
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/css2?family=Inter", nil))
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%q", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestFontCSSProxyClassifiesRejectedClientQueryWithoutLoggingIt(t *testing.T) {
	var output bytes.Buffer
	proxy := NewFontCSSProxy(fontCSSProxyConfig("http://cache.invalid", "https://fonts.example"), nil)
	proxy.storage = &fakeCacheStore{getErr: io.EOF}
	proxy.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("invalid family")),
		}, nil
	})}
	proxy.logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))

	rawQuery := "family=Definitely+Invalid"
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/fonts/css2?"+rawQuery, nil))

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2: %q", len(lines), output.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &record); err != nil {
		t.Fatal(err)
	}
	if record["level"] != "WARN" || record["query_hash"] != proxy.hashQuery(rawQuery) {
		t.Fatalf("unexpected upstream log: %#v", record)
	}
	if strings.Contains(output.String(), rawQuery) {
		t.Fatalf("raw query leaked into logs: %q", output.String())
	}
}

func TestFontCSSProxyIgnoresCacheWriteFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("body{}"))
	}))
	defer upstream.Close()
	cfg := fontCSSProxyConfig("http://cache.invalid", upstream.URL)
	proxy := NewFontCSSProxy(cfg, nil)
	proxy.storage = &fakeCacheStore{getErr: io.EOF, putErr: io.ErrClosedPipe}
	proxy.runBackground = runSynchronously
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/css2?family=Inter", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "body{}" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestFontCSSProxyServesCachedCSSAndRejectsBadRequests(t *testing.T) {
	t.Parallel()

	query := "family=Inter"
	cacheKey := "cache/font-css/" + fontCSSCacheVersion + "/" + (&FontCSSProxy{}).hashQuery(query) + ".css"
	s3 := newProxyFakeMinioServer(t, map[string]proxyFakeS3Object{
		cacheKey: {body: "cached-css", contentType: "text/css"},
	})
	defer s3.Close()

	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	cfg := fontCSSProxyConfig(s3.URL, upstream.URL)
	minioClient, err := storage.NewMinioClient(cfg)
	if err != nil {
		t.Fatalf("NewMinioClient returned error: %v", err)
	}
	proxy := NewFontCSSProxy(cfg, minioClient)

	req := httptest.NewRequest(http.MethodGet, "/fonts/css2?family=Inter", nil)
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "cached-css" {
		t.Fatalf("cached response status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Result().Header.Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}

	rec = httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/css2", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing query status = %d", rec.Code)
	}
}

type fontCSSMemoryCache struct {
	objects map[string][]byte
	getKeys []string
	putKeys []string
}

func (s *fontCSSMemoryCache) Get(_ context.Context, key string) ([]byte, string, error) {
	s.getKeys = append(s.getKeys, key)
	data, ok := s.objects[key]
	if !ok {
		return nil, "", io.EOF
	}
	return append([]byte(nil), data...), "text/css; charset=utf-8", nil
}

func (s *fontCSSMemoryCache) Put(_ context.Context, key string, data []byte, _ string) error {
	s.putKeys = append(s.putKeys, key)
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

func fontCSSProxyConfig(s3URL string, upstreamURL string) *config.Config {
	return &config.Config{
		S3Endpoint:        strings.TrimPrefix(s3URL, "http://"),
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3Region:          "us-east-1",
		S3CacheBucket:     "cache",
		FontCSSUpstream:   upstreamURL,
		FontCacheMaxAge:   3600,
		CDNPublicURL:      "https://cdn.example",
		AllowedOrigins:    []string{"https://app.example"},
	}
}
