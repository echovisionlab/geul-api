//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestRuntimeEditorFileUploadIsIndependentAndFileScoped(t *testing.T) {
	stack := testutil.SetupSharedRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	fileClient := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)
	audioBytes, err := os.ReadFile(testutil.RepositoryTestAudioMP3(t))
	require.NoError(t, err)
	fileID, delivery := completeRuntimeEditorMediaUploadAndWait(
		t,
		stack,
		fileClient,
		manager,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		"audio/mpeg",
		runtimeTestFileName("independent-editor-audio.mp3"),
		audioBytes,
		func(delivery *commonv1.MediaDelivery) bool {
			return delivery.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY &&
				delivery.GetPlayback().GetUrl() != "" &&
				delivery.GetSpectrogram().GetUrl() != "" &&
				delivery.GetWaveform().GetUrl() != ""
		},
	)
	require.NotEmpty(t, fileID)
	require.NotEmpty(t, delivery.GetPlayback().GetUrl())
	var binding model.FileIngestBinding
	require.NoError(t, stack.DB.Where("file_id = ?", fileID).Take(&binding).Error)
	require.Equal(t, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(), binding.UploadType)
	require.Empty(t, binding.EntityID)
	require.Nil(t, binding.EntityType)
	var usageCount int64
	require.NoError(t, stack.DB.Table("content_block_attachment").Where("file_id = ?", fileID).Count(&usageCount).Error)
	require.Zero(t, usageCount, "upload completion must not attach the independent File to a document")
}

type runtimeRemoteImportResolver struct {
	ip net.IP
}

func (r runtimeRemoteImportResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return []net.IP{append(net.IP(nil), r.ip...)}, nil
}

func TestRuntimeEditorFileRemoteImportIsIndependent(t *testing.T) {
	stack := testutil.SetupSharedRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())

	body := []byte("runtime remote editor File\n")
	remoteServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "media.example.com", request.Host)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, err := w.Write(body)
		require.NoError(t, err)
	}))
	t.Cleanup(remoteServer.Close)
	remoteServerURL, err := url.Parse(remoteServer.URL)
	require.NoError(t, err)

	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	confirmedPublisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	fileService := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		confirmedPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	fileService.remoteImportResolver = runtimeRemoteImportResolver{ip: net.ParseIP("8.8.8.8")}
	fileService.remoteImportDialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remoteServerURL.Host)
	}
	remoteTransport, ok := remoteServer.Client().Transport.(*http.Transport)
	require.True(t, ok)
	fileService.remoteImportBaseTransport = remoteTransport

	correlationID := uuid.NewString()
	request := connect.NewRequest(&managev1.DownloadFromUrlRequest{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		Url:           "https://media.example.com/runtime-attachment.txt",
		CorrelationId: &correlationID,
	})
	response, err := fileService.DownloadFromUrl(
		auth.WithUser(context.Background(), manager.AuthUserInfo()),
		request,
	)
	require.NoError(t, err)
	require.NotEmpty(t, response.Msg.FileId)
	require.Empty(t, response.Msg.GetSlotId())
	canonicalFileName := response.Msg.GetDelivery().GetFileName()
	require.NotEmpty(t, canonicalFileName)

	stored := requireRuntimeCanonicalFileRecord(
		t,
		stack.DB,
		response.Msg.FileId,
		response.Msg.GetDelivery().GetExtension(),
		"text/plain",
		body,
	)
	expectedSHA := sha256.Sum256(body)
	require.Equal(t, expectedSHA[:], stored.SHA256)
	var binding model.FileIngestBinding
	require.NoError(t, stack.DB.Where("file_id = ?", response.Msg.FileId).Take(&binding).Error)
	require.Equal(t, managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT.String(), binding.UploadType)
	require.Empty(t, binding.EntityID)
	require.Nil(t, binding.EntityType)
	var usageCount int64
	require.NoError(t, stack.DB.Table("content_block_attachment").Where("file_id = ?", response.Msg.FileId).Count(&usageCount).Error)
	require.Zero(t, usageCount)
}

func uploadMultipartPart(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
) uploadPartResponse {
	t.Helper()
	return uploadMultipartPartWithCorrelation(t, baseURL, user, fileID, uploadID, partNumber, body, "")
}

func uploadMultipartBody(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	session *managev1.InitiateMultipartUploadResponse,
	body []byte,
) {
	t.Helper()

	chunkSize := int(session.GetChunkSize())
	require.Positive(t, chunkSize)
	totalParts := int(session.GetTotalParts())
	require.Positive(t, totalParts)
	require.Equal(t, expectedUploadPartCount(len(body), chunkSize), totalParts)

	for partNumber := 1; partNumber <= totalParts; partNumber++ {
		partBody := multipartPartBody(t, session, body, partNumber)
		response := uploadMultipartPart(
			t,
			baseURL,
			user,
			session.GetFileId(),
			session.GetUploadId(),
			partNumber,
			partBody,
		)
		require.NotEmpty(t, response.ETag)
	}
}

func multipartPartBody(
	t *testing.T,
	session *managev1.InitiateMultipartUploadResponse,
	body []byte,
	partNumber int,
) []byte {
	t.Helper()

	chunkSize := int(session.GetChunkSize())
	require.Positive(t, chunkSize)
	totalParts := int(session.GetTotalParts())
	require.Positive(t, totalParts)
	require.GreaterOrEqual(t, partNumber, 1)
	require.LessOrEqual(t, partNumber, totalParts)
	require.Equal(t, expectedUploadPartCount(len(body), chunkSize), totalParts)

	start := (partNumber - 1) * chunkSize
	require.Less(t, start, len(body))
	end := start + chunkSize
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}

func expectedUploadPartCount(bodySize int, chunkSize int) int {
	if bodySize <= 0 {
		return 1
	}
	return (bodySize + chunkSize - 1) / chunkSize
}

func waitForRuntimeMediaDelivery(
	t *testing.T,
	fileClient managev1connect.FileServiceClient,
	user *testutil.OryUser,
	fileID string,
	ready func(*commonv1.MediaDelivery) bool,
) *commonv1.MediaDelivery {
	t.Helper()

	var last *commonv1.MediaDelivery
	testutil.WaitForFileProcessingComplete(t, 3*time.Minute, func() (bool, string, error) {
		getReq := connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID})
		setAuthHeaders(getReq.Header(), user)

		getResp, err := fileClient.GetMediaDelivery(context.Background(), getReq)
		if err != nil {
			return false, "", err
		}
		last = getResp.Msg.GetDelivery()
		if last == nil {
			return false, "delivery is nil", nil
		}

		msg := fmt.Sprintf(
			"processing status=%s percentage=%v playback=%q thumbnail=%q spectrogram=%q waveform=%q",
			last.GetProcessingStatus().String(),
			last.ProcessingPercentage,
			last.GetPlayback().GetUrl(),
			last.GetThumbnail().GetUrl(),
			last.GetSpectrogram().GetUrl(),
			last.GetWaveform().GetUrl(),
		)

		return ready(last), msg, nil
	})
	require.NotNil(t, last)
	return last
}

func completeRuntimeEditorMediaUploadAndWait(
	t *testing.T,
	stack *testutil.RuntimeStack,
	fileClient managev1connect.FileServiceClient,
	manager *testutil.OryUser,
	uploadType managev1.UploadType,
	mimeType string,
	fileName string,
	body []byte,
	ready func(*commonv1.MediaDelivery) bool,
) (string, *commonv1.MediaDelivery) {
	t.Helper()

	fileLastModified := time.Now().UnixMilli()
	initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       uploadType,
		FileSize:         int64(len(body)),
		MimeType:         mimeType,
		FileName:         fileName,
		FileLastModified: &fileLastModified,
	})
	setAuthHeaders(initReq.Header(), manager)

	initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
	require.NoError(t, err)

	uploadMultipartBody(t, stack.BackendURL, manager, initResp.Msg, body)
	completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:        initResp.Msg.FileId,
		UploadId:      initResp.Msg.UploadId,
		CorrelationId: runtimePtr(uuid.NewString()),
	})
	setAuthHeaders(completeReq.Header(), manager)

	completeResp, err := fileClient.CompleteMultipartUpload(context.Background(), completeReq)
	require.NoError(t, err)
	require.Equal(t, initResp.Msg.FileId, completeResp.Msg.FileId)
	require.Equal(t, initResp.Msg.Extension, completeResp.Msg.GetDelivery().GetExtension())
	requireCanonicalInlineRef(t, completeResp.Msg.GetDelivery().GetInline(), initResp.Msg.FileId, initResp.Msg.Extension, mimeType)
	requireCanonicalDownloadRef(t, completeResp.Msg.GetDelivery().GetDownload(), initResp.Msg.FileId, initResp.Msg.Extension, mimeType)
	stored := requireRuntimeCanonicalFileRecord(t, stack.DB, initResp.Msg.FileId, initResp.Msg.Extension, mimeType, body)
	require.Empty(t, stored.SHA256)

	delivery := waitForRuntimeMediaDelivery(t, fileClient, manager, initResp.Msg.FileId, ready)
	return initResp.Msg.FileId, delivery
}

func uploadMultipartPartWithCorrelation(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
	correlationID string,
) uploadPartResponse {
	t.Helper()

	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	if correlationID != "" {
		query.Set("correlationId", correlationID)
	}
	client := &http.Client{Timeout: 30 * time.Second}

	if partNumber == 1 {
		prefixQuery := url.Values{}
		prefixQuery.Set("fileId", fileID)
		prefixQuery.Set("uploadId", uploadID)
		if correlationID != "" {
			prefixQuery.Set("correlationId", correlationID)
		}
		prefixSize := min(len(body), multipartSniffBytes)
		prefixReq, err := http.NewRequest(
			http.MethodPost,
			fmt.Sprintf("%s/upload/prefix?%s", baseURL, prefixQuery.Encode()),
			bytes.NewReader(body[:prefixSize]),
		)
		require.NoError(t, err)
		setAuthHeaders(prefixReq.Header, user)
		prefixReq.ContentLength = int64(prefixSize)
		prefixResp, err := client.Do(prefixReq)
		require.NoError(t, err)
		defer prefixResp.Body.Close()
		prefixResponseBody, err := io.ReadAll(prefixResp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, prefixResp.StatusCode, string(prefixResponseBody))
	}

	presignReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/presign?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(presignReq.Header, user)
	presignResp, err := client.Do(presignReq)
	require.NoError(t, err)
	defer presignResp.Body.Close()
	require.Equal(t, http.StatusOK, presignResp.StatusCode)
	var presigned multipartPresignResponse
	require.NoError(t, json.NewDecoder(presignResp.Body).Decode(&presigned))
	require.NotEmpty(t, presigned.URL)

	putReq, err := http.NewRequest(http.MethodPut, presigned.URL, bytes.NewReader(body))
	require.NoError(t, err)
	putReq.ContentLength = int64(len(body))
	putResp, err := client.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	putResponseBody, err := io.ReadAll(putResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, putResp.StatusCode, string(putResponseBody))

	confirmReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/confirm?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(confirmReq.Header, user)
	confirmResp, err := client.Do(confirmReq)
	require.NoError(t, err)
	defer confirmResp.Body.Close()
	require.Equal(t, http.StatusOK, confirmResp.StatusCode)

	var payload uploadPartResponse
	require.NoError(t, json.NewDecoder(confirmResp.Body).Decode(&payload))
	require.NotEmpty(t, payload.ETag)
	return payload
}

func newFileIngestSignalReceiver(t *testing.T, dsn string) *fileIngestSignalReceiver {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), `LISTEN "file.ingest"`)
	require.NoError(t, err)
	receiver := &fileIngestSignalReceiver{conn: conn}
	t.Cleanup(func() {
		_ = receiver.conn.Close(context.Background())
	})
	return receiver
}

func waitForFileIngestProtoEvent[T proto.Message](
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	newEvent func() T,
	match func(T) bool,
) T {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	wantedType := string(newEvent().ProtoReflect().Descriptor().FullName())

	for {
		notification, err := receiver.conn.WaitForNotification(ctx)
		if err != nil {
			t.Fatalf("timed out waiting for matching file ingest event: %v", err)
			var zero T
			return zero
		}
		var envelope eventpkg.Envelope
		require.NoError(t, json.Unmarshal([]byte(notification.Payload), &envelope))
		if envelope.MessageType != wantedType {
			continue
		}
		body, err := envelope.Payload()
		require.NoError(t, err)
		event := newEvent()
		require.NoError(t, proto.Unmarshal(body, event))
		if match(event) {
			return event
		}
	}
}

func waitForFileIngestUploadEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	match func(*managev1.FileIngestUploadEvent) bool,
) *managev1.FileIngestUploadEvent {
	t.Helper()
	return waitForFileIngestProtoEvent(
		t,
		receiver,
		timeout,
		func() *managev1.FileIngestUploadEvent { return &managev1.FileIngestUploadEvent{} },
		match,
	)
}

func waitForFileIngestFinalizedEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	match func(*managev1.FileIngestFinalizedEvent) bool,
) *managev1.FileIngestFinalizedEvent {
	t.Helper()
	return waitForFileIngestProtoEvent(
		t,
		receiver,
		timeout,
		func() *managev1.FileIngestFinalizedEvent { return &managev1.FileIngestFinalizedEvent{} },
		match,
	)
}

func waitForFileIngestAttachedEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	match func(*managev1.FileIngestAttachedEvent) bool,
) *managev1.FileIngestAttachedEvent {
	t.Helper()
	return waitForFileIngestProtoEvent(
		t,
		receiver,
		timeout,
		func() *managev1.FileIngestAttachedEvent { return &managev1.FileIngestAttachedEvent{} },
		match,
	)
}

func requireNoFileIngestFailedEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	duration time.Duration,
	match func(*managev1.FileIngestFailedEvent) bool,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), duration)
	defer cancel()
	wantedType := string((&managev1.FileIngestFailedEvent{}).ProtoReflect().Descriptor().FullName())

	for {
		notification, err := receiver.conn.WaitForNotification(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return
		}
		require.NoError(t, err)
		var envelope eventpkg.Envelope
		require.NoError(t, json.Unmarshal([]byte(notification.Payload), &envelope))
		if envelope.MessageType != wantedType {
			continue
		}
		body, err := envelope.Payload()
		require.NoError(t, err)
		event := &managev1.FileIngestFailedEvent{}
		require.NoError(t, proto.Unmarshal(body, event))
		require.Falsef(t, match(event), "unexpected file ingest failed event: %v", event)
	}
}

func setAuthHeaders(header http.Header, user *testutil.OryUser) {
	testutil.ApplyAuthHeaders(header, user)
}

func runtimePtr[T interface{}](value T) *T {
	return &value
}

func runtimeTestFileName(base string) string {
	return uuid.NewString() + "-" + base
}

func runtimeDeref[T interface{}](value *T) T {
	var zero T
	if value == nil {
		return zero
	}
	return *value
}

func countUploadSessions(t *testing.T, db *gorm.DB, uploadID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("upload_session").Where("upload_id = ?", uploadID).Count(&count).Error)
	return count
}

func countUploadParts(t *testing.T, db *gorm.DB, uploadID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("upload_part").Where("upload_id = ?", uploadID).Count(&count).Error)
	return count
}

func loadUploadPartInfosForTest(t *testing.T, db *gorm.DB, uploadID string) []*managev1.UploadPartInfo {
	t.Helper()

	var parts []model.UploadPart
	require.NoError(t, db.Where("upload_id = ?", uploadID).Order("part_number ASC").Find(&parts).Error)
	return uploadPartInfos(parts)
}

func requireUploadSessionStatus(t *testing.T, db *gorm.DB, uploadID string, expected managev1.UploadSessionStatus) {
	t.Helper()

	var session model.UploadSession
	require.NoError(t, db.Where("upload_id = ?", uploadID).First(&session).Error)
	require.Equal(t, expected, uploadSessionStatusToProto(session.Status))
}

func countFilesByID(t *testing.T, db *gorm.DB, fileID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	return count
}

func requireRuntimeCanonicalFileRecord(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	extension string,
	mimeType string,
	body []byte,
) model.File {
	t.Helper()

	var file model.File
	require.NoError(t, db.Where("id = ?", fileID).Take(&file).Error)
	require.Equal(t, extension, file.Extension)
	require.Equal(t, mimeType, file.MimeType)
	require.EqualValues(t, len(body), file.FileSize)
	return file
}

func runtimeS3Client(t *testing.T, stack *testutil.RuntimeStack) *s3.Client {
	t.Helper()

	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(stack.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(stack.S3AccessKeyID, stack.S3SecretAccessKey, ""),
		),
	)
	require.NoError(t, err)

	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(stack.S3Endpoint)
		o.UsePathStyle = stack.S3ForcePathStyle
	})
	return s3Client
}

func runtimeS3ObjectExists(t *testing.T, s3Client *s3.Client, bucket string, key string) bool {
	t.Helper()

	_, err := s3Client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true
	}
	if isRuntimeS3NotFound(err) {
		return false
	}
	require.NoError(t, err)
	return false
}

func runtimeS3PrefixHasObjects(t *testing.T, s3Client *s3.Client, bucket string, prefix string) bool {
	t.Helper()

	out, err := s3Client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	require.NoError(t, err)
	return len(out.Contents) > 0
}

func isRuntimeS3NotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotFound", "NoSuchKey", "404":
		return true
	default:
		return false
	}
}
