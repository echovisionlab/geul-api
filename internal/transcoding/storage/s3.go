// Package storage provides the S3-compatible object storage adapter.
package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// completionMetadataKey identifies the terminal receipt stored with a
// completed output object.
const completionMetadataKey = "geul-completion-v1"

// Options contains only the object-storage settings required by this adapter.
type Options struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

// S3Client wraps the AWS S3 client with helper methods
type S3Client struct {
	client *s3.Client
	bucket string
	files  localFileSystem
}

type localFile interface {
	io.Reader
	io.Writer
	io.Closer
	Stat() (os.FileInfo, error)
}

type localFileSystem interface {
	MkdirAll(string, os.FileMode) error
	Create(string) (localFile, error)
	Open(string) (localFile, error)
}

type osFileSystem struct{}

func (osFileSystem) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func (osFileSystem) Create(path string) (localFile, error)        { return os.Create(path) }
func (osFileSystem) Open(path string) (localFile, error)          { return os.Open(path) }

type awsConfigLoader func(context.Context, ...func(*config.LoadOptions) error) (aws.Config, error)

// NewS3Client creates a new S3 client
func NewS3Client(options Options) (*S3Client, error) {
	return newS3Client(options, config.LoadDefaultConfig)
}

func newS3Client(options Options, loadConfig awsConfigLoader) (*S3Client, error) {
	endpoint := options.Endpoint

	awsCfg, err := loadConfig(context.Background(),
		config.WithRegion(options.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			options.AccessKeyID,
			options.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = options.ForcePathStyle
	})

	slog.Info("S3 client initialized",
		"endpoint", endpoint,
		"bucket", options.Bucket,
	)

	return &S3Client{
		client: client,
		bucket: options.Bucket,
		files:  osFileSystem{},
	}, nil
}

// Download downloads a file from S3 to a local path
func (c *S3Client) Download(ctx context.Context, key string, localPath string) error {
	// Ensure directory exists
	dir := filepath.Dir(localPath)
	if err := c.files.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Get object from S3
	result, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to get object: %w", err)
	}
	defer func() { _ = result.Body.Close() }()

	// Create local file
	file, err := c.files.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer func() { _ = file.Close() }()

	// Copy content
	written, err := io.Copy(file, result.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	slog.Debug("Downloaded file from S3",
		"key", key,
		"local_path", localPath,
		"size", written,
	)

	return nil
}

// Upload uploads a local file to S3
func (c *S3Client) Upload(ctx context.Context, key string, localPath string, contentType string) error {
	return c.upload(ctx, key, localPath, contentType, nil)
}

// UploadCompleted atomically stores the final output object and the durable
// terminal result used to identify a repeated command after process restarts.
func (c *S3Client) UploadCompleted(
	ctx context.Context,
	key, localPath, contentType string,
	completion []byte,
) error {
	if len(completion) == 0 {
		return fmt.Errorf("completion payload is required")
	}
	return c.upload(ctx, key, localPath, contentType, map[string]string{
		completionMetadataKey: base64.StdEncoding.EncodeToString(completion),
	})
}

func (c *S3Client) upload(
	ctx context.Context,
	key, localPath, contentType string,
	metadata map[string]string,
) error {
	file, err := c.files.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer func() { _ = file.Close() }()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	_, err = c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          file,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(stat.Size()),
		Metadata:      metadata,
	})
	if err != nil {
		return fmt.Errorf("failed to upload object: %w", err)
	}

	return nil
}

// Completion reads a terminal result attached to a final output object. The
// result shares the output object's lifecycle and does not require a separate
// delivery ledger.
func (c *S3Client) Completion(ctx context.Context, key string) ([]byte, bool, error) {
	result, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NotFound", "NoSuchKey", "NoSuchObject":
				return nil, false, nil
			}
		}
		return nil, false, fmt.Errorf("failed to inspect completion object: %w", err)
	}
	encoded := result.Metadata[completionMetadataKey]
	if encoded == "" {
		return nil, false, nil
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("invalid completion metadata: %w", err)
	}
	return payload, true, nil
}
