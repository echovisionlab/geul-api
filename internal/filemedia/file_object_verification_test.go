package filemedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func TestReadStoredObjectPrefixUsesBoundedRange(t *testing.T) {
	prefix := bytes.Repeat([]byte{0x5a}, multipartSniffBytes)
	service := newObjectVerificationService(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/media-bucket/media/file-id.webp", r.URL.Path)
		require.Equal(t, "bytes=0-65535", r.Header.Get("Range"))
		w.Header().Set("Content-Length", fmt.Sprint(len(prefix)))
		w.Header().Set("Content-Range", "bytes 0-65535/8589934592")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(prefix)
	})

	actual, err := service.readStoredObjectPrefix(context.Background(), "media/file-id.webp", 8*1024*1024*1024)
	require.NoError(t, err)
	require.Equal(t, prefix, actual)
}

func TestReadStoredObjectPrefixReadsWholeSmallObject(t *testing.T) {
	body := []byte("small image")
	service := newObjectVerificationService(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "bytes=0-10", r.Header.Get("Range"))
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Content-Range", "bytes 0-10/11")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	})

	actual, err := service.readStoredObjectPrefix(context.Background(), "media/file-id.webp", int64(len(body)))
	require.NoError(t, err)
	require.Equal(t, body, actual)
}

func TestReadStoredObjectPrefixRejectsFetchFailure(t *testing.T) {
	service := newObjectVerificationService(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	})

	_, err := service.readStoredObjectPrefix(context.Background(), "media/missing.webp", 1)
	require.ErrorContains(t, err, "get completed object prefix")
}

func TestReadStoredObjectPrefixRejectsTruncatedBody(t *testing.T) {
	service := newObjectVerificationService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Header().Set("Content-Range", "bytes 0-99/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("truncated"))
	})

	_, err := service.readStoredObjectPrefix(context.Background(), "media/truncated.webp", 100)
	require.ErrorContains(t, err, "read completed object prefix")
}

func newObjectVerificationService(t *testing.T, handler http.HandlerFunc) *FileService {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := s3.NewFromConfig(aws.Config{
		Region:           "us-east-1",
		Credentials:      credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:       server.Client(),
		RetryMaxAttempts: 1,
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	return &FileService{s3Client: client, s3Bucket: "media-bucket"}
}

func TestReadStoredObjectPrefixFetchFailureIsNotMIMEMismatch(t *testing.T) {
	service := newObjectVerificationService(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary storage failure", http.StatusServiceUnavailable)
	})

	_, err := service.readStoredObjectPrefix(context.Background(), "media/file-id.webp", 1)
	require.Error(t, err)
	require.False(t, errors.Is(err, errStoredObjectMIMEMismatch))
}
