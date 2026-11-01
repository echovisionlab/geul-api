//go:build integration

package filemedia

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type runtimeRemoteImportResolver struct {
	ip net.IP
}

func (r runtimeRemoteImportResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return []net.IP{append(net.IP(nil), r.ip...)}, nil
}

func TestEditorFileRemoteImportIsIndependentDirectIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupDirectMediaRuntimeStack(t)
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
	fileService := newTestService(Deps{
		DB:             stack.DB,
		S3:             runtimeS3Client(t, stack),
		AsyncPublisher: confirmedPublisher,
		S3Bucket:       stack.S3MediaBucket,
		CDNDomain:      stack.CDNURL,
		MediaDomain:    stack.MediaURL,
		MediaSecret:    stack.MediaSigningSecret,
		Transcoder:     &recordingFileTranscoderPublisher{},
		SpiceDB:        stack.SpiceDBClient,
	})
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
	response, err := NewConnectHandler(fileService).DownloadFromUrl(
		auditedUserContext(context.Background(), manager.AuthUserInfo()),
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

func requireUploadSessionStatus(t *testing.T, db *gorm.DB, uploadID string, expected managev1.UploadSessionStatus) {
	t.Helper()

	var session model.UploadSession
	require.NoError(t, db.Where("upload_id = ?", uploadID).First(&session).Error)
	require.Equal(t, expected, uploadSessionStatusToProto(domain.UploadSessionStatus(session.Status)))
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
