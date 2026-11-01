package filemedia

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitiateMultipartUploadRejectsInvalidFilenameBeforeS3MutationUnit(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount++
	}))
	t.Cleanup(server.Close)
	client := s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:  server.Client(),
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	svc := &Service{
		s3Client:      client,
		s3Bucket:      "media-bucket",
		uploadConfigs: cloneUploadConfigs(),
	}
	admin := newUnitUser()
	svc.spiceDB = authztest.AllowAll().Client(t)
	ctx := auditedUserContext(context.Background(), admin.AuthUserInfo())
	_, err := NewConnectHandler(svc).InitiateMultipartUpload(ctx, connect.NewRequest(
		&managev1.InitiateMultipartUploadRequest{
			UploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			FileName:   "../logo.png",
			FileSize:   1024,
			MimeType:   "image/png",
		},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	assert.Zero(t, requestCount)
}
