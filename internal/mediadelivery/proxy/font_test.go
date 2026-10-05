package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
)

func TestFontProxyServesCachedFont(t *testing.T) {
	t.Parallel()

	s3 := newProxyFakeMinioServer(t, map[string]proxyFakeS3Object{
		"cache/fonts/roboto.woff2": {body: "font-bytes", contentType: "font/woff2"},
	})
	defer s3.Close()

	cfg := fontProxyConfig(s3.URL, "")
	minioClient, err := storage.NewMinioClient(cfg)
	if err != nil {
		t.Fatalf("NewMinioClient returned error: %v", err)
	}

	proxy := NewFontProxy(cfg, minioClient)
	proxy.runBackground = runSynchronously
	req := httptest.NewRequest(http.MethodGet, "/fonts/roboto.woff2", nil)
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "font-bytes" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, string(body))
	}
	if got := resp.Header.Get("Content-Type"); got != "font/woff2" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestFontProxyFetchesUpstreamOnCacheMiss(t *testing.T) {
	t.Parallel()

	s3 := newProxyFakeMinioServer(t, map[string]proxyFakeS3Object{})
	defer s3.Close()

	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		w.Header().Set("Content-Type", "font/woff2")
		_, _ = w.Write([]byte("fresh-font"))
	}))
	defer upstream.Close()

	cfg := fontProxyConfig(s3.URL, upstream.URL)
	minioClient, err := storage.NewMinioClient(cfg)
	if err != nil {
		t.Fatalf("NewMinioClient returned error: %v", err)
	}

	rec := httptest.NewRecorder()
	proxy := NewFontProxy(cfg, minioClient)
	cache := &fakeCacheStore{getErr: io.EOF}
	proxy.storage = cache
	proxy.runBackground = runSynchronously
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/s/inter/v1/font.woff2", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "fresh-font" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if upstreamPath != "/s/inter/v1/font.woff2" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if cache.putCalls != 1 {
		t.Fatalf("cache put calls = %d", cache.putCalls)
	}
}

func TestFontProxyUpstreamFailureModesAndDefaultContentType(t *testing.T) {
	tests := []struct {
		name        string
		upstreamURL string
		transport   http.RoundTripper
		wantType    string
		wantErr     bool
	}{
		{"invalid URL", "http://[::1", nil, "", true},
		{"transport error", "https://fonts.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		}), "", true},
		{"read error", "https://fonts.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &errorReadCloser{err: io.ErrUnexpectedEOF}}, nil
		}), "", true},
		{"default content type", "https://fonts.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("font"))}, nil
		}), "font/woff2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := NewFontProxy(&config.Config{FontUpstreamURL: tt.upstreamURL}, nil)
			if tt.transport != nil {
				proxy.client = &http.Client{Transport: tt.transport}
			}
			_, contentType, err := proxy.fetchFromUpstream(t.Context(), "font.woff2")
			if (err != nil) != tt.wantErr || contentType != tt.wantType {
				t.Fatalf("contentType=%q err=%v", contentType, err)
			}
		})
	}
}

func TestFontProxyIgnoresCacheWriteFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("font"))
	}))
	defer upstream.Close()
	cfg := fontProxyConfig("http://cache.invalid", upstream.URL)
	proxy := NewFontProxy(cfg, nil)
	proxy.storage = &fakeCacheStore{getErr: io.EOF, putErr: io.ErrClosedPipe}
	proxy.runBackground = runSynchronously
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/font.woff2", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "font" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestFontProxyRejectsEmptyPathAndUpstreamErrors(t *testing.T) {
	t.Parallel()

	s3 := newProxyFakeMinioServer(t, map[string]proxyFakeS3Object{})
	defer s3.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := fontProxyConfig(s3.URL, upstream.URL)
	minioClient, err := storage.NewMinioClient(cfg)
	if err != nil {
		t.Fatalf("NewMinioClient returned error: %v", err)
	}
	proxy := NewFontProxy(cfg, minioClient)

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty path status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/missing.woff2", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream error status = %d", rec.Code)
	}
}

func fontProxyConfig(s3URL string, upstreamURL string) *config.Config {
	if upstreamURL == "" {
		upstreamURL = "https://fonts.example"
	}
	return &config.Config{
		S3Endpoint:        strings.TrimPrefix(s3URL, "http://"),
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3Region:          "us-east-1",
		S3CacheBucket:     "cache",
		FontS3Prefix:      "fonts/",
		FontUpstreamURL:   upstreamURL,
		FontCacheMaxAge:   3600,
		AllowedOrigins:    []string{"https://app.example"},
	}
}
