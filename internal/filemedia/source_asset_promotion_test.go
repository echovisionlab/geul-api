package filemedia

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestValidateSourceAssetPromotionFileEnforcesKindMediaContract(t *testing.T) {
	digest := sha256.Sum256([]byte("verified-source"))
	for _, tc := range []struct {
		name      string
		kind      string
		extension string
		mimeType  string
		wantError bool
	}{
		{name: "image webp", kind: "image", extension: "webp", mimeType: "image/webp"},
		{name: "image rejects PDF", kind: "image", extension: "pdf", mimeType: "application/pdf", wantError: true},
		{name: "mesh GLB", kind: "mesh", extension: "glb", mimeType: "model/gltf-binary"},
		{name: "mesh rejects image", kind: "mesh", extension: "webp", mimeType: "image/webp", wantError: true},
		{name: "waveform JSON", kind: "waveform", extension: "json", mimeType: "application/json"},
		{name: "waveform rejects PNG", kind: "waveform", extension: "png", mimeType: "image/png", wantError: true},
		{name: "spectrogram PNG", kind: "spectrogram", extension: "png", mimeType: "image/png"},
		{name: "thumbnail rejects PNG", kind: "thumbnail", extension: "png", mimeType: "image/png", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSourceAssetPromotionFile(File{
				MimeType:  tc.mimeType,
				FileSize:  128,
				Extension: tc.extension,
				SHA256:    digest[:],
			}, tc.kind)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type sourceAssetPromotionS3Fixture struct {
	client          *s3.Client
	mu              sync.Mutex
	objects         map[string][]byte
	mimeTypes       map[string]string
	failTransfer    bool
	transferStarted chan struct{}
	releaseTransfer chan struct{}
	transferOnce    sync.Once
	deleted         []string
}

func newSourceAssetPromotionS3Fixture(t *testing.T, file model.File, body []byte) *sourceAssetPromotionS3Fixture {
	t.Helper()
	fixture := &sourceAssetPromotionS3Fixture{objects: map[string][]byte{}, mimeTypes: map[string]string{}}
	sourceKey, err := mediaauth.MediaObjectKey(file.ID, file.Extension)
	require.NoError(t, err)
	fixture.objects[sourceKey] = append([]byte(nil), body...)
	fixture.mimeTypes[sourceKey] = file.MimeType
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	fixture.client = s3.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient: server.Client(), Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	return fixture
}

func (f *sourceAssetPromotionS3Fixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/media-bucket/")
	switch r.Method {
	case http.MethodPut:
		f.mu.Lock()
		failTransfer := f.failTransfer
		transferStarted := f.transferStarted
		releaseTransfer := f.releaseTransfer
		f.mu.Unlock()
		if transferStarted != nil {
			f.transferOnce.Do(func() { close(transferStarted) })
			<-releaseTransfer
		}
		if failTransfer {
			http.Error(w, "injected promotion write failure", http.StatusInternalServerError)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read promotion body", http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.objects[key] = append([]byte(nil), body...)
		f.mimeTypes[key] = r.Header.Get("Content-Type")
		w.Header().Set("ETag", `"test"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		f.mu.Lock()
		body, ok := f.objects[key]
		mimeType := f.mimeTypes[key]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Content-Type", mimeType)
	case http.MethodGet:
		f.mu.Lock()
		body, ok := f.objects[key]
		mimeType := f.mimeTypes[key]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Content-Type", mimeType)
		_, _ = io.Copy(w, bytes.NewReader(body))
	case http.MethodDelete:
		f.mu.Lock()
		f.deleted = append(f.deleted, key)
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}
}

func (f *sourceAssetPromotionS3Fixture) setFailTransfer(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failTransfer = fail
}

func (f *sourceAssetPromotionS3Fixture) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}
