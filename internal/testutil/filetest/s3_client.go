package filetest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewS3Client routes S3 calls to handler and disables SDK retries.
func NewS3Client(t *testing.T, handler http.Handler) *s3.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	awsConfig := aws.Config{
		Region:      "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", "")),
		HTTPClient:  server.Client(),
		Retryer: func() aws.Retryer {
			return aws.NopRetryer{}
		},
	}
	return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
}
