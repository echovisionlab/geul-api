package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
)

type fakeS3Object struct {
	body        string
	contentType string
}

func newFakeMinioServer(t *testing.T, objects map[string]fakeS3Object) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodHead:
			obj, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", obj.contentType)
			w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
			w.Header().Set("ETag", `"test-etag"`)
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		case http.MethodGet:
			obj, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", obj.contentType)
			w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
			w.Header().Set("ETag", `"test-etag"`)
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			_, _ = io.WriteString(w, obj.body)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			objects[key] = fakeS3Object{
				body:        string(body),
				contentType: r.Header.Get("Content-Type"),
			}
			w.Header().Set("ETag", `"test-etag"`)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
}

func minioConfigForServer(server *httptest.Server) *config.Config {
	return &config.Config{
		S3Endpoint:        strings.TrimPrefix(server.URL, "http://"),
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3Region:          "us-east-1",
		S3UseSSL:          false,
		S3CacheBucket:     "cache",
	}
}

func TestMinioClientRoundTripsAgainstS3CompatibleHTTP(t *testing.T) {
	t.Parallel()

	objects := map[string]fakeS3Object{
		"cache/existing.txt": {
			body:        "cached object",
			contentType: "text/plain",
		},
	}
	server := newFakeMinioServer(t, objects)
	defer server.Close()

	client, err := NewMinioClient(minioConfigForServer(server))
	if err != nil {
		t.Fatalf("NewMinioClient returned error: %v", err)
	}

	if client.Client() == nil {
		t.Fatal("expected underlying MinIO client")
	}

	ctx := context.Background()
	if err := client.Put(ctx, "uploaded.json", []byte(`{"ok":true}`), "application/json"); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if got := objects["cache/uploaded.json"].body; !strings.Contains(got, `{"ok":true}`) {
		t.Fatalf("uploaded body = %q", got)
	}

	data, contentType, err := client.Get(ctx, "existing.txt")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if string(data) != "cached object" || contentType != "text/plain" {
		t.Fatalf("Get returned body=%q contentType=%q", string(data), contentType)
	}

}

func TestMinioClientConstructorsRejectInvalidEndpoint(t *testing.T) {
	cfg := &config.Config{S3Endpoint: "http://[::1", S3AccessKeyID: "access", S3SecretAccessKey: "secret"}
	if _, err := NewMinioClient(cfg); err == nil {
		t.Fatal("NewMinioClient accepted invalid endpoint")
	}
}

func TestMinioClientGetErrors(t *testing.T) {
	server := newFakeMinioServer(t, map[string]fakeS3Object{})
	defer server.Close()
	client, err := NewMinioClient(minioConfigForServer(server))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Get(t.Context(), "missing.txt"); err == nil {
		t.Fatal("Get returned nil error for missing object")
	}
	client.bucket = ""
	if _, _, err := client.Get(t.Context(), "object.txt"); err == nil {
		t.Fatal("Get returned nil error for invalid bucket")
	}
}

func TestMinioClientGetReturnsBodyReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "10")
		w.Header().Set("ETag", `"test-etag"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "short")
		}
	}))
	defer server.Close()
	client, err := NewMinioClient(minioConfigForServer(server))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Get(t.Context(), "truncated.bin"); err == nil {
		t.Fatal("Get returned nil error for truncated body")
	}
}
