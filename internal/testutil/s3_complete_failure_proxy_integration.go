//go:build integration

package testutil

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type runtimeS3CompleteFailureProxy struct {
	server *httptest.Server
	proxy  *httputil.ReverseProxy

	mu            sync.Mutex
	markedUploads map[string]struct{}
	failureCounts map[string]int
}

type runtimeS3ErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	UploadID  string   `xml:"UploadId"`
	RequestID string   `xml:"RequestId"`
	HostID    string   `xml:"HostId"`
}

func newRuntimeS3CompleteFailureProxy(t *testing.T, targetEndpoint string) *runtimeS3CompleteFailureProxy {
	t.Helper()

	target, err := url.Parse(targetEndpoint)
	require.NoError(t, err)
	require.Contains(t, []string{"http", "https"}, target.Scheme)
	require.NotEmpty(t, target.Host)

	failureProxy := &runtimeS3CompleteFailureProxy{
		proxy:         httputil.NewSingleHostReverseProxy(target),
		markedUploads: make(map[string]struct{}),
		failureCounts: make(map[string]int),
	}
	failureProxy.proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
		http.Error(w, fmt.Sprintf("S3 test proxy upstream failure: %v", proxyErr), http.StatusBadGateway)
	}
	failureProxy.server = httptest.NewServer(http.HandlerFunc(failureProxy.serveHTTP))
	registerIntegrationCleanup(t, "runtime S3 completion failure proxy", func() error {
		failureProxy.server.Close()
		return nil
	})
	return failureProxy
}

func (p *runtimeS3CompleteFailureProxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	uploadID := strings.TrimSpace(r.URL.Query().Get("uploadId"))
	if r.Method == http.MethodPost && uploadID != "" && p.shouldFail(uploadID) {
		p.writeNoSuchUpload(w, uploadID)
		return
	}

	p.proxy.ServeHTTP(w, r)
}

func (p *runtimeS3CompleteFailureProxy) shouldFail(uploadID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.markedUploads[uploadID]; !ok {
		return false
	}
	p.failureCounts[uploadID]++
	return true
}

func (p *runtimeS3CompleteFailureProxy) markUpload(uploadID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markedUploads[uploadID] = struct{}{}
}

func (p *runtimeS3CompleteFailureProxy) failureCount(uploadID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failureCounts[uploadID]
}

func (p *runtimeS3CompleteFailureProxy) writeNoSuchUpload(w http.ResponseWriter, uploadID string) {
	const requestID = "geul-integration-complete-failure"

	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-amz-request-id", requestID)
	w.WriteHeader(http.StatusNotFound)
	_ = xml.NewEncoder(w).Encode(runtimeS3ErrorResponse{
		Code:      "NoSuchUpload",
		Message:   "The specified multipart upload does not exist.",
		UploadID:  uploadID,
		RequestID: requestID,
		HostID:    "geul-integration",
	})
}

func (s *RuntimeStack) MarkMultipartCompletionFailure(t *testing.T, uploadID string) {
	t.Helper()
	require.NotNil(t, s.s3CompleteFailureProxy, "runtime stack does not have the S3 completion failure proxy")
	require.NotEmpty(t, strings.TrimSpace(uploadID))
	s.s3CompleteFailureProxy.markUpload(uploadID)
}

func (s *RuntimeStack) MultipartCompletionFailureCount(t *testing.T, uploadID string) int {
	t.Helper()
	require.NotNil(t, s.s3CompleteFailureProxy, "runtime stack does not have the S3 completion failure proxy")
	return s.s3CompleteFailureProxy.failureCount(uploadID)
}
