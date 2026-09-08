package proxy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/storage"
	"github.com/minio/minio-go/v7"
)

type fakeDeliveryStore struct {
	stat      minio.ObjectInfo
	statErr   error
	body      string
	openErr   error
	readErr   error
	statCalls int
	openCalls int
	lastKey   string
	lastRange *byteRange
}

func newMinioClientForProxyTest(t *testing.T, serverURL string) *minio.Client {
	t.Helper()
	client, err := storage.NewMinioClient(&config.Config{
		S3Endpoint:        strings.TrimPrefix(serverURL, "http://"),
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3Region:          "us-east-1",
		S3CacheBucket:     "cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.Client()
}

func (s *fakeDeliveryStore) Stat(_ context.Context, objectKey string) (minio.ObjectInfo, error) {
	s.statCalls++
	s.lastKey = objectKey
	return s.stat, s.statErr
}

func (s *fakeDeliveryStore) Open(
	_ context.Context,
	objectKey string,
	requestedRange *byteRange,
) (io.ReadCloser, error) {
	s.openCalls++
	s.lastKey = objectKey
	s.lastRange = requestedRange
	if s.openErr != nil {
		return nil, s.openErr
	}
	if s.readErr != nil {
		return &errorReadCloser{err: s.readErr}, nil
	}
	body := s.body
	if requestedRange != nil {
		body = body[requestedRange.start : requestedRange.end+1]
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

type errorReadCloser struct {
	err error
}

func (r *errorReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r *errorReadCloser) Close() error {
	return nil
}

func TestMinioDeliveryStoreStatAndOpen(t *testing.T) {
	server := newProxyFakeMinioServer(t, map[string]proxyFakeS3Object{
		"media/media/11111111-1111-4111-8111-111111111111.bin": {
			body:        "0123456789",
			contentType: "application/octet-stream",
		},
	})
	defer server.Close()

	client := newMinioClientForProxyTest(t, server.URL)
	store := newMinioDeliveryStore(client, "media")
	ctx := context.Background()
	info, err := store.Stat(ctx, "media/11111111-1111-4111-8111-111111111111.bin")
	if err != nil || info.Size != 10 {
		t.Fatalf("Stat() info=%#v err=%v", info, err)
	}

	for _, requestedRange := range []*byteRange{nil, {start: 2, end: 5}} {
		body, err := store.Open(ctx, "media/11111111-1111-4111-8111-111111111111.bin", requestedRange)
		if err != nil {
			t.Fatalf("Open(%#v) error = %v", requestedRange, err)
		}
		data, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("readErr=%v closeErr=%v", readErr, closeErr)
		}
		want := "0123456789"
		if requestedRange != nil {
			want = "2345"
		}
		if string(data) != want {
			t.Fatalf("Open(%#v) = %q, want %q", requestedRange, data, want)
		}
	}
}

func TestErrorReadCloser(t *testing.T) {
	want := errors.New("read failed")
	reader := &errorReadCloser{err: want}
	if _, err := reader.Read(nil); !errors.Is(err, want) {
		t.Fatalf("Read() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
