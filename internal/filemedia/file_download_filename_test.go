//go:build integration

package filemedia

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeNewDownloadFilenameUnit(t *testing.T) {
	t.Parallel()

	got, err := normalizeNewDownloadFilename("  간월재.wav  ")
	require.NoError(t, err)
	assert.Equal(t, "간월재.wav", got)

	for _, value := range []string{
		"",
		" ",
		".",
		"..",
		"folder/file.wav",
		`folder\file.wav`,
		"line\nbreak.wav",
		strings.Repeat("x", maxDownloadFilenameBytes+1),
		string([]byte{0xff}),
	} {
		_, err := normalizeNewDownloadFilename(value)
		assert.Error(t, err, value)
	}
}

func TestCanonicalDownloadFilenameFallsBackForHistoricalInvalidValuesUnit(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	invalid := "../source.wav"
	assert.Equal(
		t,
		"download-"+fileID+".wav",
		CanonicalDownloadFilename(&invalid, fileID, "wav"),
	)
	valid := " source.wav "
	assert.Equal(t, "source.wav", CanonicalDownloadFilename(&valid, fileID, "wav"))
}

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
	svc := &FileService{
		s3Client:      client,
		s3Bucket:      "media-bucket",
		uploadConfigs: cloneUploadConfigs(),
	}
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	svc.spiceDB = stack.SpiceDBClient
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	_, err := svc.InitiateMultipartUpload(ctx, connect.NewRequest(
		&managev1.InitiateMultipartUploadRequest{
			UploadType: managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			FileName:   "../logo.png",
			FileSize:   1024,
			MimeType:   "image/png",
		},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Zero(t, requestCount)
}

func TestCanonicalRemoteImportFilenameNormalizesExtensionAndEscapedSeparatorsUnit(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	assert.Equal(
		t,
		"cover.webp",
		canonicalRemoteImportFilename("cover.jpg", fileID, "image/webp"),
	)
	assert.Equal(
		t,
		"download-"+fileID+".wav",
		canonicalRemoteImportFilename("folder/file.aif", fileID, "audio/wav"),
	)
}
