//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type multipartListPartsFailureTransport struct {
	base http.RoundTripper
}

func (t *multipartListPartsFailureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet && req.URL.Query().Get("uploadId") != "" {
		return nil, fmt.Errorf("injected ListParts failure")
	}
	return t.base.RoundTrip(req)
}

func TestMultipartMissingPartsRemainRetryableAndAbortableDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	body, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	require.NotEmpty(t, body)
	observer := &multipartCompletionObservingTransport{}
	service := newEditorUploadSerializationFileService(stack, observingRuntimeS3Client(t, stack, observer))

	retrySession := initiateMultipartRecoveryUpload(t, ctx, service, body)
	setMultipartDetectedMime(t, stack, retrySession.GetUploadId(), "image/jpeg")
	type completionResult struct {
		response *connect.Response[managev1.CompleteMultipartUploadResponse]
		err      error
	}
	completionResults := make(chan completionResult, 2)
	for attempt := 0; attempt < 2; attempt++ {
		go func() {
			response, completeErr := service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
				UploadId: retrySession.GetUploadId(),
				FileId:   retrySession.GetFileId(),
			}))
			completionResults <- completionResult{response: response, err: completeErr}
		}()
	}
	for attempt := 0; attempt < 2; attempt++ {
		result := <-completionResults
		require.Error(t, result.err)
		require.Nil(t, result.response)
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(result.err))
		require.Contains(t, result.err.Error(), "uploaded part count")
	}
	requireEditorUploadSessionStatus(t, stack.DB, retrySession.GetUploadId(), model.UploadSessionStatusInitiated)
	require.Zero(t, observer.count(), "invalid completion attempts must not reach object-store completion")

	handleMultipartPartDirect(t, service, manager, retrySession.GetFileId(), retrySession.GetUploadId(), 1, body)
	completed, err := service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: retrySession.GetUploadId(),
		FileId:   retrySession.GetFileId(),
	}))
	require.NoError(t, err)
	require.Equal(t, retrySession.GetFileId(), completed.Msg.GetFileId())
	require.Equal(t, 1, observer.count(), "one validated retry must issue one object-store completion")
	requireEditorUploadSessionAbsent(t, stack.DB, retrySession.GetUploadId())

	abortSession := initiateMultipartRecoveryUpload(t, ctx, service, body)
	setMultipartDetectedMime(t, stack, abortSession.GetUploadId(), "image/jpeg")
	_, err = service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: abortSession.GetUploadId(),
		FileId:   abortSession.GetFileId(),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	aborted, err := service.AbortMultipartUpload(ctx, connect.NewRequest(&managev1.AbortMultipartUploadRequest{
		UploadId: abortSession.GetUploadId(),
		FileId:   abortSession.GetFileId(),
	}))
	require.NoError(t, err)
	require.True(t, aborted.Msg.GetSuccess())
	requireEditorUploadSessionAbsent(t, stack.DB, abortSession.GetUploadId())
}

func TestMultipartPartReplacementRequiresFreshConfirmationDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	body, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	baseS3 := runtimeS3Client(t, stack)
	baseService := newEditorUploadSerializationFileService(stack, baseS3)
	initiated := initiateMultipartRecoveryUpload(t, ctx, baseService, body)
	setMultipartDetectedMime(t, stack, initiated.GetUploadId(), "image/jpeg")
	handleMultipartPartDirect(t, baseService, manager, initiated.GetFileId(), initiated.GetUploadId(), 1, body)

	session := loadEditorUploadSession(t, stack.DB, initiated.GetUploadId())
	objectKey, err := uploadSessionObjectKey(session)
	require.NoError(t, err)
	var originalPart model.UploadPart
	require.NoError(t, stack.DB.First(
		&originalPart,
		"upload_id = ? AND part_number = 1",
		session.UploadID,
	).Error)
	t.Cleanup(func() {
		_, _ = baseS3.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(stack.S3MediaBucket),
			Key:      aws.String(objectKey),
			UploadId: aws.String(session.UploadID),
		})
		_ = stack.DB.Delete(&model.UploadSession{}, "upload_id = ?", session.UploadID).Error
	})

	replacementBody := append([]byte(nil), body...)
	replacementBody[len(replacementBody)-1] ^= 0xff
	query := url.Values{}
	query.Set("fileId", session.FileID)
	query.Set("uploadId", session.UploadID)
	query.Set("partNumber", "1")
	presignReq := httptest.NewRequest(http.MethodPost, "/upload/part/presign?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(presignReq.Header, manager)
	presignRec := httptest.NewRecorder()
	auth.RequireGatewaySession(baseService.db, http.HandlerFunc(baseService.HandlePresignUploadPart)).ServeHTTP(presignRec, presignReq)
	require.Equal(t, http.StatusOK, presignRec.Code, presignRec.Body.String())
	var presigned multipartPresignResponse
	require.NoError(t, json.NewDecoder(presignRec.Body).Decode(&presigned))
	require.NotEmpty(t, presigned.URL)

	var confirmedDuringReplacement int64
	require.NoError(t, stack.DB.Model(&model.UploadPart{}).
		Where("upload_id = ? AND part_number = 1", session.UploadID).
		Count(&confirmedDuringReplacement).Error)
	require.Zero(t, confirmedDuringReplacement, "presign must fence the stale confirmed ETag before replacement")

	_, err = baseService.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: session.UploadID,
		FileId:   session.FileID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	putReq, err := http.NewRequest(http.MethodPut, presigned.URL, bytes.NewReader(replacementBody))
	require.NoError(t, err)
	putReq.ContentLength = int64(len(replacementBody))
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	confirmReq := httptest.NewRequest(http.MethodPost, "/upload/part/confirm?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(confirmReq.Header, manager)
	confirmRec := httptest.NewRecorder()
	auth.RequireGatewaySession(baseService.db, http.HandlerFunc(baseService.HandleConfirmUploadPart)).ServeHTTP(confirmRec, confirmReq)
	require.Equal(t, http.StatusOK, confirmRec.Code, confirmRec.Body.String())
	var replacement uploadPartResponse
	require.NoError(t, json.NewDecoder(confirmRec.Body).Decode(&replacement))
	require.NotEmpty(t, replacement.ETag)
	require.NotEqual(t, originalPart.ETag, replacement.ETag)

	completed, err := baseService.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: session.UploadID,
		FileId:   session.FileID,
	}))
	require.NoError(t, err)
	require.Equal(t, session.FileID, completed.Msg.GetFileId())
	var stored model.File
	require.NoError(t, stack.DB.First(&stored, "id = ?", session.FileID).Error)
	require.Empty(t, stored.SHA256)
}

func TestMultipartCompletionUsesActualPartAfterConfirmedPresignIsReusedDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	body, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	s3Client := runtimeS3Client(t, stack)
	service := newEditorUploadSerializationFileService(stack, s3Client)
	initiated := initiateMultipartRecoveryUpload(t, ctx, service, body)
	verifyDirectMultipartPrefix(t, service, manager, initiated.GetFileId(), initiated.GetUploadId(), body)
	presignedURL := presignDirectMultipartPart(t, service, manager, initiated.GetFileId(), initiated.GetUploadId(), 1)
	putDirectMultipartPart(t, presignedURL, body)
	confirmDirectMultipartPart(t, service, manager, initiated.GetFileId(), initiated.GetUploadId(), 1)

	// UploadPart URLs remain valid until their short expiry. Reusing the same
	// URL after confirmation replaces the object-store part without updating
	// the database cache, so completion must list the actual ETag again.
	replacement := append([]byte(nil), body...)
	replacement[len(replacement)-1] ^= 0xff
	putDirectMultipartPart(t, presignedURL, replacement)

	completed, err := service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: initiated.GetUploadId(),
		FileId:   initiated.GetFileId(),
	}))
	require.NoError(t, err)
	require.Equal(t, initiated.GetFileId(), completed.Msg.GetFileId())

	var stored model.File
	require.NoError(t, stack.DB.First(&stored, "id = ?", initiated.GetFileId()).Error)
	require.Empty(t, stored.SHA256)
}

func TestMultipartCompletionRejectsBytesDifferentFromVerifiedPrefixDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	verifiedBody, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	s3Client := runtimeS3Client(t, stack)
	service := newEditorUploadSerializationFileService(stack, s3Client)
	initiated := initiateMultipartRecoveryUpload(t, ctx, service, verifiedBody)
	verifyDirectMultipartPrefix(t, service, manager, initiated.GetFileId(), initiated.GetUploadId(), verifiedBody)
	presignedURL := presignDirectMultipartPart(t, service, manager, initiated.GetFileId(), initiated.GetUploadId(), 1)

	replacement := make([]byte, len(verifiedBody))
	copy(replacement, []byte("%PDF-1.7\n"))
	putDirectMultipartPart(t, presignedURL, replacement)
	confirmDirectMultipartPart(t, service, manager, initiated.GetFileId(), initiated.GetUploadId(), 1)

	_, err = service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: initiated.GetUploadId(),
		FileId:   initiated.GetFileId(),
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "completed object MIME")
	requireEditorUploadSessionStatus(t, stack.DB, initiated.GetUploadId(), model.UploadSessionStatusFailed)

	session := loadEditorUploadSession(t, stack.DB, initiated.GetUploadId())
	objectKey, keyErr := uploadSessionObjectKey(session)
	require.NoError(t, keyErr)
	require.False(t, runtimeS3ObjectExists(t, s3Client, stack.S3MediaBucket, objectKey))
}

func TestInterruptedMultipartResumeReusesConfirmedPartsAndCompletesDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	fixtureBytes, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	firstPart := make([]byte, chunkSize)
	copy(firstPart, fixtureBytes)
	secondPart := append([]byte(nil), fixtureBytes...)
	fileSize := int64(len(firstPart) + len(secondPart))

	baseS3 := runtimeS3Client(t, stack)
	service := newEditorUploadSerializationFileService(stack, baseS3)
	fileLastModified := time.Now().UnixMilli()
	initiated, err := service.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileName:         "multipart-resume.jpg",
		FileSize:         fileSize,
		MimeType:         "image/jpeg",
		FileLastModified: &fileLastModified,
	}))
	require.NoError(t, err)
	require.False(t, initiated.Msg.GetResumed())
	require.Equal(t, int32(2), initiated.Msg.GetTotalParts())
	findCandidate := func() *connect.Request[managev1.FindMultipartUploadCandidateRequest] {
		return connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
			UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
			FileId:     hardCutPtrString(initiated.Msg.GetFileId()),
			UploadId:   hardCutPtrString(initiated.Msg.GetUploadId()),
		})
	}

	// DB-only divergence: a stale cache row is not a completed part when S3
	// has no matching bytes. Resume keeps the exact session identity, returns
	// the empty S3 snapshot, and removes the stale cache fact.
	now := time.Now().UTC()
	require.NoError(t, stack.DB.Create(&model.UploadPart{
		UploadID:   initiated.Msg.GetUploadId(),
		PartNumber: 1,
		ETag:       "stale-db-only-etag",
		Size:       int64(len(firstPart)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}).Error)
	dbOnlyResumed, err := service.FindMultipartUploadCandidate(ctx, findCandidate())
	require.NoError(t, err)
	require.Equal(t, initiated.Msg.GetUploadId(), dbOnlyResumed.Msg.GetUploadId())
	require.Equal(t, initiated.Msg.GetFileId(), dbOnlyResumed.Msg.GetFileId())
	require.Empty(t, dbOnlyResumed.Msg.GetUploadedParts())
	var stalePartCount int64
	require.NoError(t, stack.DB.Model(&model.UploadPart{}).
		Where("upload_id = ?", initiated.Msg.GetUploadId()).
		Count(&stalePartCount).Error)
	require.Zero(t, stalePartCount)

	// Simulate a process stopping after S3 accepted a part but before the API
	// wrote its upload_part cache. Detected MIME is recorded before the S3 call
	// on the real handler path, while S3 remains authoritative for actual parts.
	session := loadEditorUploadSession(t, stack.DB, initiated.Msg.GetUploadId())
	setMultipartDetectedMime(t, stack, session.UploadID, "image/jpeg")
	objectKey, err := uploadSessionObjectKey(session)
	require.NoError(t, err)
	orphaned, err := baseS3.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(stack.S3MediaBucket),
		Key:           aws.String(objectKey),
		UploadId:      aws.String(session.UploadID),
		PartNumber:    aws.Int32(1),
		Body:          bytes.NewReader(firstPart),
		ContentLength: aws.Int64(int64(len(firstPart))),
	})
	require.NoError(t, err)
	require.NotNil(t, orphaned.ETag)
	orphanedETag := strings.Trim(*orphaned.ETag, "\"")
	require.NotEmpty(t, orphanedETag)

	crashResumed, err := service.FindMultipartUploadCandidate(ctx, findCandidate())
	require.NoError(t, err)
	require.Equal(t, initiated.Msg.GetUploadId(), crashResumed.Msg.GetUploadId())
	require.Equal(t, initiated.Msg.GetFileId(), crashResumed.Msg.GetFileId())
	require.Equal(t, []*managev1.UploadPartInfo{{PartNumber: 1, Etag: orphanedETag}}, crashResumed.Msg.GetUploadedParts())
	var reconciledPart model.UploadPart
	require.NoError(t, stack.DB.First(
		&reconciledPart,
		"upload_id = ? AND part_number = 1",
		initiated.Msg.GetUploadId(),
	).Error)
	require.Equal(t, orphanedETag, reconciledPart.ETag)
	require.Equal(t, int64(len(firstPart)), reconciledPart.Size)
	var partCount int64
	require.NoError(t, stack.DB.Model(&model.UploadPart{}).
		Where("upload_id = ?", initiated.Msg.GetUploadId()).
		Count(&partCount).Error)
	require.Equal(t, int64(1), partCount)

	second := handleMultipartPartDirect(
		t,
		service,
		manager,
		initiated.Msg.GetFileId(),
		initiated.Msg.GetUploadId(),
		2,
		secondPart,
	)
	reloaded, err := service.FindMultipartUploadCandidate(ctx, findCandidate())
	require.NoError(t, err)
	require.Equal(t, []*managev1.UploadPartInfo{
		{PartNumber: 1, Etag: orphanedETag},
		{PartNumber: 2, Etag: second.ETag},
	}, reloaded.Msg.GetUploadedParts())

	completed, err := service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: initiated.Msg.GetUploadId(),
		FileId:   initiated.Msg.GetFileId(),
	}))
	require.NoError(t, err)
	require.Equal(t, initiated.Msg.GetFileId(), completed.Msg.GetFileId())
	requireEditorUploadSessionAbsent(t, stack.DB, initiated.Msg.GetUploadId())
}

func TestMultipartResumeListPartsFailureDoesNotUseStaleDBCacheDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	body, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	baseS3 := runtimeS3Client(t, stack)
	baseService := newEditorUploadSerializationFileService(stack, baseS3)
	initiated := initiateMultipartRecoveryUpload(t, ctx, baseService, body)
	findCandidate := func() *connect.Request[managev1.FindMultipartUploadCandidateRequest] {
		return connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
			UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
			FileId:     hardCutPtrString(initiated.GetFileId()),
			UploadId:   hardCutPtrString(initiated.GetUploadId()),
		})
	}
	part := handleMultipartPartDirect(
		t,
		baseService,
		manager,
		initiated.GetFileId(),
		initiated.GetUploadId(),
		1,
		body,
	)

	options := baseS3.Options()
	options.HTTPClient = &http.Client{Transport: &multipartListPartsFailureTransport{
		base: http.DefaultTransport.(*http.Transport).Clone(),
	}}
	failingService := newEditorUploadSerializationFileService(stack, s3.New(options))
	resumed, err := failingService.FindMultipartUploadCandidate(ctx, findCandidate())
	require.Error(t, err)
	require.Nil(t, resumed)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Contains(t, err.Error(), "list multipart resume parts")

	var cached model.UploadPart
	require.NoError(t, stack.DB.First(
		&cached,
		"upload_id = ? AND part_number = 1",
		initiated.GetUploadId(),
	).Error)
	require.Equal(t, part.ETag, cached.ETag, "failed ListParts must not replace or silently trust the stale cache")

	require.NoError(t, baseService.abortUploadSession(ctx, loadEditorUploadSession(t, stack.DB, initiated.GetUploadId()), "test cleanup"))
}

func TestMultipartInvalidCompletedObjectDeleteFailureRetainsCleanupAuthorityDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	body, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	baseS3 := runtimeS3Client(t, stack)
	fault := &multipartDeleteFailureTransport{base: http.DefaultTransport.(*http.Transport).Clone(), failDeletes: true}
	options := baseS3.Options()
	options.HTTPClient = &http.Client{Transport: fault}
	service := newEditorUploadSerializationFileService(stack, s3.New(options))
	initiated := initiateMultipartRecoveryUpload(t, ctx, service, body)
	handleMultipartPartDirect(t, service, manager, initiated.GetFileId(), initiated.GetUploadId(), 1, body)
	session := loadEditorUploadSession(t, stack.DB, initiated.GetUploadId())
	var uploadedPart model.UploadPart
	require.NoError(t, stack.DB.First(&uploadedPart, "upload_id = ? AND part_number = 1", session.UploadID).Error)
	objectKey, err := uploadSessionObjectKey(session)
	require.NoError(t, err)
	_, err = baseS3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(stack.S3MediaBucket),
		Key:      aws.String(objectKey),
		UploadId: aws.String(session.UploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{
			PartNumber: aws.Int32(uploadedPart.PartNumber),
			ETag:       aws.String(uploadedPart.ETag),
		}}},
	})
	require.NoError(t, err)
	_, err = baseS3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(stack.S3MediaBucket),
		Key:         aws.String(objectKey),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("image/png"),
	})
	require.NoError(t, err)

	_, err = service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: session.UploadID,
		FileId:   session.FileID,
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "delete invalid completed object")
	requireEditorUploadSessionStatus(t, stack.DB, session.UploadID, model.UploadSessionStatusFinalizing)
	require.True(t, runtimeS3ObjectExists(t, baseS3, stack.S3MediaBucket, objectKey))
	require.GreaterOrEqual(t, fault.failureCount(), 1)

	fault.allowDeletes()
	_, err = service.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		UploadId: session.UploadID,
		FileId:   session.FileID,
	}))
	require.Error(t, err)
	requireEditorUploadSessionStatus(t, stack.DB, session.UploadID, model.UploadSessionStatusFailed)
	require.False(t, runtimeS3ObjectExists(t, baseS3, stack.S3MediaBucket, objectKey))
}

func initiateMultipartRecoveryUpload(
	t *testing.T,
	ctx context.Context,
	service *FileService,
	body []byte,
) *managev1.InitiateMultipartUploadResponse {
	t.Helper()
	response, err := service.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileName:   "multipart-recovery.jpg",
		FileSize:   int64(len(body)),
		MimeType:   "image/jpeg",
	}))
	require.NoError(t, err)
	return response.Msg
}

func verifyDirectMultipartPrefix(
	t *testing.T,
	service *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	body []byte,
) {
	t.Helper()
	query := url.Values{"fileId": {fileID}, "uploadId": {uploadID}}
	prefixSize := min(len(body), multipartSniffBytes)
	req := httptest.NewRequest(http.MethodPost, "/upload/prefix?"+query.Encode(), bytes.NewReader(body[:prefixSize]))
	testutil.ApplyAuthHeaders(req.Header, user)
	req.ContentLength = int64(prefixSize)
	rec := httptest.NewRecorder()
	auth.RequireGatewaySession(service.db, http.HandlerFunc(service.HandleVerifyUploadPrefix)).ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

func presignDirectMultipartPart(
	t *testing.T,
	service *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
) string {
	t.Helper()
	query := url.Values{
		"fileId":     {fileID},
		"uploadId":   {uploadID},
		"partNumber": {fmt.Sprintf("%d", partNumber)},
	}
	req := httptest.NewRequest(http.MethodPost, "/upload/part/presign?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(req.Header, user)
	rec := httptest.NewRecorder()
	auth.RequireGatewaySession(service.db, http.HandlerFunc(service.HandlePresignUploadPart)).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload multipartPresignResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	require.NotEmpty(t, payload.URL)
	return payload.URL
}

func putDirectMultipartPart(t *testing.T, presignedURL string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, presignedURL, bytes.NewReader(body))
	require.NoError(t, err)
	req.ContentLength = int64(len(body))
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
}

func confirmDirectMultipartPart(
	t *testing.T,
	service *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
) {
	t.Helper()
	query := url.Values{
		"fileId":     {fileID},
		"uploadId":   {uploadID},
		"partNumber": {fmt.Sprintf("%d", partNumber)},
	}
	req := httptest.NewRequest(http.MethodPost, "/upload/part/confirm?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(req.Header, user)
	rec := httptest.NewRecorder()
	auth.RequireGatewaySession(service.db, http.HandlerFunc(service.HandleConfirmUploadPart)).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func setMultipartDetectedMime(t *testing.T, stack *testutil.RuntimeStack, uploadID string, mimeType string) {
	t.Helper()
	require.NoError(t, stack.DB.Model(&model.UploadSession{}).
		Where("upload_id = ?", uploadID).
		Update("detected_mime", mimeType).Error)
}

type multipartDeleteFailureTransport struct {
	base http.RoundTripper

	mu          sync.Mutex
	failDeletes bool
	failures    int
}

func (transport *multipartDeleteFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodDelete || request.URL.Query().Get("uploadId") != "" || !transport.shouldFailDelete() {
		return transport.base.RoundTrip(request)
	}
	body, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: "AccessDenied", Message: "injected completed-object delete failure"})
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

func (transport *multipartDeleteFailureTransport) shouldFailDelete() bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if !transport.failDeletes {
		return false
	}
	transport.failures++
	return true
}

func (transport *multipartDeleteFailureTransport) allowDeletes() {
	transport.mu.Lock()
	transport.failDeletes = false
	transport.mu.Unlock()
}

func (transport *multipartDeleteFailureTransport) failureCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.failures
}
