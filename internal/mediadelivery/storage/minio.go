package storage

import (
	"bytes"
	"context"
	"io"

	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinioClient struct {
	client *minio.Client
	bucket string
}

func NewMinioClient(cfg *config.Config) (*MinioClient, error) {
	return newMinioClient(cfg, cfg.S3AccessKeyID, cfg.S3SecretAccessKey, cfg.S3CacheBucket)
}

func newMinioClient(cfg *config.Config, accessKey string, secretKey string, bucket string) (*MinioClient, error) {
	client, err := minio.New(cfg.S3Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:       cfg.S3UseSSL,
		Region:       cfg.S3Region,
		BucketLookup: minio.BucketLookupPath, // Force path style
	})
	if err != nil {
		return nil, err
	}

	return &MinioClient{
		client: client,
		bucket: bucket,
	}, nil
}

func (m *MinioClient) Get(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", err
	}
	defer obj.Close()

	info, err := obj.Stat()
	if err != nil {
		return nil, "", err
	}

	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, "", err
	}

	return data, info.ContentType, nil
}

func (m *MinioClient) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := m.client.PutObject(ctx, m.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

// Client returns the underlying minio client
func (m *MinioClient) Client() *minio.Client {
	return m.client
}
