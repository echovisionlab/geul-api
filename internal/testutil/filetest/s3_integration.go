//go:build integration

package filetest

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/config"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func NewS3(t *testing.T) (*s3.Client, *config.Config) {
	t.Helper()
	client, cfg, err := NewS3FromSharedLease(t.Context())
	require.NoError(t, err)
	return client, cfg
}

func NewS3FromSharedLease(ctx context.Context) (*s3.Client, *config.Config, error) {
	cfg, err := sharedS3Config()
	if err != nil {
		return nil, nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID,
			cfg.S3SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("load shared file integration S3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(cfg.S3Endpoint)
		options.UsePathStyle = cfg.S3ForcePathStyle
	})
	// Each caller gets its own bucket so prefix listings only see objects the
	// test wrote. The MinIO container is discarded with the suite.
	cfg.S3Bucket = "it-file-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(cfg.S3Bucket)}); err != nil {
		return nil, nil, fmt.Errorf("create file integration S3 bucket %s: %w", cfg.S3Bucket, err)
	}
	return client, cfg, nil
}

func sharedS3Config() (*config.Config, error) {
	backend, err := pgtest.CurrentAppIntegrationBackendLease()
	if err != nil {
		return nil, fmt.Errorf("load shared file integration backend lease: %w", err)
	}
	cfg := &config.Config{
		S3Bucket:          backend.S3MediaBucket,
		S3Region:          backend.S3Region,
		S3Endpoint:        backend.S3Endpoint,
		S3AccessKeyID:     backend.S3AccessKeyID,
		S3SecretAccessKey: backend.S3SecretAccessKey,
		S3ForcePathStyle:  backend.S3ForcePathStyle,
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "bucket", value: cfg.S3Bucket},
		{name: "region", value: cfg.S3Region},
		{name: "endpoint", value: cfg.S3Endpoint},
		{name: "access key ID", value: cfg.S3AccessKeyID},
		{name: "secret access key", value: cfg.S3SecretAccessKey},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("shared file integration S3 %s is required", field.name)
		}
	}
	return cfg, nil
}

func PutObject(ctx context.Context, s3Client *s3.Client, bucket, key, body string) error {
	_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte(body)),
	})
	return err
}

func ListKeys(t *testing.T, ctx context.Context, s3Client *s3.Client, bucket, prefix string) []string {
	t.Helper()

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	var keys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		require.NoError(t, err)
		for _, obj := range page.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
	}

	sort.Strings(keys)
	return keys
}
