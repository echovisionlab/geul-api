package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

type failingLocalFileSystem struct {
	mkdirErr  error
	createErr error
	openFile  localFile
	openErr   error
}

func (f failingLocalFileSystem) MkdirAll(string, os.FileMode) error { return f.mkdirErr }
func (f failingLocalFileSystem) Create(string) (localFile, error)   { return nil, f.createErr }
func (f failingLocalFileSystem) Open(string) (localFile, error)     { return f.openFile, f.openErr }

type statErrorFile struct{ bytes.Buffer }

func (*statErrorFile) Close() error               { return nil }
func (*statErrorFile) Stat() (os.FileInfo, error) { return nil, errors.New("stat") }

type s3TestServer struct {
	server   *httptest.Server
	objects  map[string][]byte
	metadata map[string]string
}

func newS3TestServer(t *testing.T) *s3TestServer {
	t.Helper()
	store := &s3TestServer{
		objects:  map[string][]byte{"media/source.mp3": []byte("source-bytes")},
		metadata: map[string]string{},
	}
	store.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.handle(w, r)
	}))
	t.Cleanup(store.server.Close)
	return store
}

func (s *s3TestServer) handle(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/")
	switch r.Method {
	case http.MethodHead:
		s.head(w, r, key)
	case http.MethodGet:
		s.get(w, r, key)
	case http.MethodPut:
		s.put(w, r, key)
	default:
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}
}

func (s *s3TestServer) head(w http.ResponseWriter, r *http.Request, key string) {
	body, ok := s.objects[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	if value := s.metadata[key]; value != "" {
		w.Header().Set("X-Amz-Meta-Geul-Completion-V1", value)
	}
	w.WriteHeader(http.StatusOK)
}

func (s *s3TestServer) get(w http.ResponseWriter, r *http.Request, key string) {
	body, ok := s.objects[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(body)
}

func (s *s3TestServer) put(w http.ResponseWriter, r *http.Request, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.objects[key] = body
	s.metadata[key] = r.Header.Get("X-Amz-Meta-Geul-Completion-V1")
	w.WriteHeader(http.StatusOK)
}

func (s *s3TestServer) client(t *testing.T) *S3Client {
	t.Helper()
	client, err := NewS3Client(testS3Config(s.server.URL))
	if err != nil {
		t.Fatalf("NewS3Client returned error: %v", err)
	}
	return client
}

func TestS3ClientDownloadsSource(t *testing.T) {
	t.Parallel()
	store := newS3TestServer(t)
	client := store.client(t)

	downloadPath := filepath.Join(t.TempDir(), "nested", "source.mp3")
	if err := client.Download(context.Background(), "source.mp3", downloadPath); err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	downloaded, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(downloaded) != "source-bytes" {
		t.Fatalf("downloaded body = %q", string(downloaded))
	}
}

func TestS3ClientUploadsFile(t *testing.T) {
	t.Parallel()
	store := newS3TestServer(t)
	client := store.client(t)
	uploadPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(uploadPath, []byte("upload-body"), 0o644); err != nil {
		t.Fatalf("write upload file: %v", err)
	}
	if err := client.Upload(context.Background(), "uploaded.txt", uploadPath, "text/plain"); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if !bytes.Contains(store.objects["media/uploaded.txt"], []byte("upload-body")) {
		t.Fatalf("uploaded body = %q", string(store.objects["media/uploaded.txt"]))
	}
}

func TestS3ClientReadsCompletionMetadata(t *testing.T) {
	t.Parallel()
	store := newS3TestServer(t)
	client := store.client(t)
	uploadPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(uploadPath, []byte("upload-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	completion := []byte("durable-result")
	if err := client.UploadCompleted(context.Background(), "completed.txt", uploadPath, "text/plain", completion); err != nil {
		t.Fatalf("UploadCompleted returned error: %v", err)
	}
	if err := client.Upload(context.Background(), "uploaded.txt", uploadPath, "text/plain"); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	loaded, found, err := client.Completion(context.Background(), "completed.txt")
	if err != nil || !found || !bytes.Equal(loaded, completion) {
		t.Fatalf("Completion = %q, %t, %v", loaded, found, err)
	}
	assertCompletionMissing(t, client, "missing.txt")
	assertCompletionMissing(t, client, "uploaded.txt")
	store.metadata["media/completed.txt"] = "not-base64"
	if _, _, err = client.Completion(context.Background(), "completed.txt"); err == nil || !strings.Contains(err.Error(), "invalid completion metadata") {
		t.Fatalf("invalid Completion error = %v", err)
	}
	if err := client.UploadCompleted(context.Background(), "empty.txt", uploadPath, "text/plain", nil); err == nil {
		t.Fatal("expected empty completion payload error")
	}
}

func assertCompletionMissing(t *testing.T, client *S3Client, key string) {
	t.Helper()
	loaded, found, err := client.Completion(context.Background(), key)
	if err != nil || found || loaded != nil {
		t.Fatalf("missing Completion = %q, %t, %v", loaded, found, err)
	}
}

func TestS3ClientWrapsHTTPFailures(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client, err := NewS3Client(testS3Config(server.URL))
	if err != nil {
		t.Fatalf("NewS3Client returned error: %v", err)
	}

	if err := client.Download(context.Background(), "missing", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected Download error")
	}
	if err := client.Upload(context.Background(), "missing", filepath.Join(t.TempDir(), "missing"), "text/plain"); err == nil {
		t.Fatal("expected Upload open error")
	}
}

func TestNewS3ClientWrapsConfigFailure(t *testing.T) {
	t.Parallel()
	_, err := newS3Client(testS3Config("http://127.0.0.1"), func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, errors.New("config")
	})
	if err == nil || !strings.Contains(err.Error(), "failed to load AWS config") {
		t.Fatalf("newS3Client error = %v", err)
	}
}

func TestS3ClientLocalFilesystemFailures(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("body"))
	}))
	defer server.Close()
	client, err := NewS3Client(testS3Config(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	client.files = failingLocalFileSystem{mkdirErr: errors.New("mkdir")}
	if err := client.Download(context.Background(), "source", "path"); err == nil || !strings.Contains(err.Error(), "create directory") {
		t.Fatalf("mkdir error = %v", err)
	}

	client.files = failingLocalFileSystem{createErr: errors.New("create")}
	if err := client.Download(context.Background(), "source", "path"); err == nil || !strings.Contains(err.Error(), "create file") {
		t.Fatalf("create error = %v", err)
	}

	client.files = failingLocalFileSystem{openFile: &statErrorFile{}}
	if err := client.Upload(context.Background(), "target", "path", "text/plain"); err == nil || !strings.Contains(err.Error(), "stat file") {
		t.Fatalf("stat error = %v", err)
	}
}

func TestS3ClientRemoteWriteFailures(t *testing.T) {
	t.Parallel()
	mode := "upload"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch mode {
		case "upload":
			http.Error(w, "failure", http.StatusInternalServerError)
		case "partial download":
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("x"))
		}
	}))
	defer server.Close()
	client, err := NewS3Client(testS3Config(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	uploadPath := filepath.Join(t.TempDir(), "upload")
	if err := os.WriteFile(uploadPath, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := client.Upload(context.Background(), "target", uploadPath, "text/plain"); err == nil {
		t.Fatal("expected upload error")
	}
	if _, _, err := client.Completion(context.Background(), "target"); err == nil || !strings.Contains(err.Error(), "inspect completion object") {
		t.Fatalf("Completion HTTP error = %v", err)
	}
	mode = "partial download"
	if err := client.Download(context.Background(), "source", filepath.Join(t.TempDir(), "source")); err == nil || !strings.Contains(err.Error(), "write file") {
		t.Fatalf("partial download error = %v", err)
	}
}

func testS3Config(endpoint string) Options {
	return Options{
		Bucket:          "media",
		Region:          "us-east-1",
		Endpoint:        endpoint,
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		ForcePathStyle:  true,
	}
}
