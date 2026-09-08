package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	appconfig "github.com/echovisionlab/geul-api/internal/assetprocessing/config"
)

type S3Client struct {
	client *s3.Client
	bucket string
}

var (
	loadDefaultConfig = config.LoadDefaultConfig
	newS3FromConfig   = s3.NewFromConfig
	mkdirAll          = os.MkdirAll
	createFile        = os.Create
	openFile          = os.Open
	copyStream        = io.Copy
	getObject         = (*s3.Client).GetObject
	putObject         = (*s3.Client).PutObject
	deleteObject      = (*s3.Client).DeleteObject
	headObject        = (*s3.Client).HeadObject
)

func NewS3Client(cfg *appconfig.Config) (*S3Client, error) {
	awsCfg, err := loadDefaultConfig(context.Background(),
		config.WithRegion(cfg.S3Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID,
			cfg.S3SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	client := newS3FromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		o.UsePathStyle = cfg.S3ForcePathStyle
	})

	slog.Info("S3 client initialized", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)
	return &S3Client{client: client, bucket: cfg.S3Bucket}, nil
}

func (c *S3Client) Download(ctx context.Context, key string, localPath string) error {
	if err := mkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}

	result, err := getObject(c.client, ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("get object %q: %w", key, err)
	}
	defer result.Body.Close()

	file, err := createFile(localPath)
	if err != nil {
		return fmt.Errorf("create local file: %w", err)
	}
	defer file.Close()

	written, err := copyStream(file, result.Body)
	if err != nil {
		return fmt.Errorf("write local file: %w", err)
	}
	slog.Debug("Downloaded object", "key", key, "path", localPath, "bytes", written)
	return nil
}

func (c *S3Client) Upload(ctx context.Context, key string, localPath string, contentType string) error {
	file, err := openFile(localPath)
	if err != nil {
		return fmt.Errorf("open upload file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat upload file: %w", err)
	}

	_, err = putObject(c.client, ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          file,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(stat.Size()),
	})
	if err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	return nil
}

func (c *S3Client) Delete(ctx context.Context, key string) error {
	_, err := deleteObject(c.client, ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}

func (c *S3Client) GetObjectSize(ctx context.Context, key string) (int64, error) {
	result, err := headObject(c.client, ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, fmt.Errorf("head object %q: %w", key, err)
	}
	if result.ContentLength == nil {
		return 0, nil
	}
	return *result.ContentLength, nil
}
