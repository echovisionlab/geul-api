//go:build integration

package filemedia

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// multipartCompletionObservingTransport counts object-store completion calls
// and can inject one ambiguous response for recovery tests.
type multipartCompletionObservingTransport struct {
	base                        http.RoundTripper
	mu                          sync.Mutex
	completeCalls               int
	returnNoSuchAfterCompletion bool
	returnedNoSuch              bool
}

func (t *multipartCompletionObservingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if req.Method != http.MethodPost || req.URL.Query().Get("uploadId") == "" {
		return response, err
	}

	t.mu.Lock()
	t.completeCalls++
	inject := t.returnNoSuchAfterCompletion && !t.returnedNoSuch && err == nil && response.StatusCode >= 200 && response.StatusCode < 300
	if inject {
		t.returnedNoSuch = true
	}
	t.mu.Unlock()
	if !inject {
		return response, err
	}

	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader(`<Error><Code>NoSuchUpload</Code><Message>injected ambiguous multipart completion</Message></Error>`)),
		Request:    req,
	}, nil
}

func (t *multipartCompletionObservingTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.completeCalls
}

func observingRuntimeS3Client(
	t *testing.T,
	stack *testutil.RuntimeStack,
	observer *multipartCompletionObservingTransport,
) *s3.Client {
	t.Helper()
	base := runtimeS3Client(t, stack)
	options := base.Options()
	observer.base = http.DefaultTransport.(*http.Transport).Clone()
	options.HTTPClient = &http.Client{Transport: observer}
	return s3.New(options)
}

type uploadPartResponse struct {
	ETag string `json:"etag"`
}

type fileIngestSignalReceiver struct {
	conn *pgx.Conn
}

func requireCanonicalDownloadRef(
	t *testing.T,
	ref *commonv1.ExpiringMediaRef,
	fileID string,
	extension string,
	mimeType string,
) {
	t.Helper()
	require.NotNil(t, ref)
	require.Equal(t, fileID, ref.GetFileId())
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD, ref.GetPurpose())
	require.Equal(t, extension, ref.GetExtension())
	require.Equal(t, mimeType, ref.GetMimeType())
	require.NotEmpty(t, ref.GetUrl())
	require.NotNil(t, ref.GetExpiresAt())
	require.True(t, ref.GetExpiresAt().AsTime().After(time.Now()))
}

func requireCanonicalInlineRef(
	t *testing.T,
	ref *commonv1.ExpiringMediaRef,
	fileID string,
	extension string,
	mimeType string,
) {
	t.Helper()
	require.NotNil(t, ref)
	require.Equal(t, fileID, ref.GetFileId())
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE, ref.GetPurpose())
	require.Equal(t, extension, ref.GetExtension())
	require.Equal(t, mimeType, ref.GetMimeType())
	require.NotEmpty(t, ref.GetUrl())
	require.NotNil(t, ref.GetExpiresAt())
	require.True(t, ref.GetExpiresAt().AsTime().After(time.Now()))
}

func handleMultipartPartDirect(
	t *testing.T,
	svc *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
) uploadPartResponse {
	t.Helper()

	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	if partNumber == 1 {
		prefixQuery := url.Values{}
		prefixQuery.Set("fileId", fileID)
		prefixQuery.Set("uploadId", uploadID)
		prefixSize := min(len(body), multipartSniffBytes)
		prefixReq := httptest.NewRequest(http.MethodPost, "/upload/prefix?"+prefixQuery.Encode(), bytes.NewReader(body[:prefixSize]))
		testutil.ApplyAuthHeaders(prefixReq.Header, user)
		prefixReq.ContentLength = int64(prefixSize)
		prefixRec := httptest.NewRecorder()
		auth.RequireGatewaySession(
			svc.db,
			http.HandlerFunc(svc.HandleVerifyUploadPrefix),
		).ServeHTTP(prefixRec, prefixReq)
		require.Equal(t, http.StatusNoContent, prefixRec.Code, prefixRec.Body.String())
	}

	presignReq := httptest.NewRequest(http.MethodPost, "/upload/part/presign?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(presignReq.Header, user)
	presignRec := httptest.NewRecorder()
	auth.RequireGatewaySession(
		svc.db,
		http.HandlerFunc(svc.HandlePresignUploadPart),
	).ServeHTTP(presignRec, presignReq)
	require.Equal(t, http.StatusOK, presignRec.Code, presignRec.Body.String())
	var presigned multipartPresignResponse
	require.NoError(t, json.NewDecoder(presignRec.Body).Decode(&presigned))
	require.NotEmpty(t, presigned.URL)

	putReq, err := http.NewRequest(http.MethodPut, presigned.URL, bytes.NewReader(body))
	require.NoError(t, err)
	putReq.ContentLength = int64(len(body))
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	confirmReq := httptest.NewRequest(http.MethodPost, "/upload/part/confirm?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(confirmReq.Header, user)
	confirmRec := httptest.NewRecorder()
	auth.RequireGatewaySession(
		svc.db,
		http.HandlerFunc(svc.HandleConfirmUploadPart),
	).ServeHTTP(confirmRec, confirmReq)
	require.Equal(t, http.StatusOK, confirmRec.Code, confirmRec.Body.String())
	var payload uploadPartResponse
	require.NoError(t, json.NewDecoder(confirmRec.Body).Decode(&payload))
	require.NotEmpty(t, payload.ETag)
	return payload
}
