//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestUploadRemoteImportObjectUsesTransferManagerWithMinIOIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	s3Client := runtimeS3Client(t, stack)
	svc := &FileService{
		s3Client: s3Client,
		s3Bucket: stack.S3MediaBucket,
	}

	body := bytes.Repeat([]byte("geul-remote-import\n"), (chunkSize/(len("geul-remote-import\n")))+65_536)
	key := "media/" + uuid.NewString() + ".bin"
	require.NoError(t, svc.uploadRemoteImportObject(context.Background(), key, bytes.NewReader(body), "application/octet-stream"))
	t.Cleanup(func() {
		_, _ = s3Client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(stack.S3MediaBucket),
			Key:    aws.String(key),
		})
	})

	object, err := s3Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(stack.S3MediaBucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	defer object.Body.Close()

	stored, err := io.ReadAll(object.Body)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(body), sha256.Sum256(stored))
	require.Equal(t, "application/octet-stream", aws.ToString(object.ContentType))
}
