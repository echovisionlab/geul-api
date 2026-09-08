package proxy

import (
	"context"
	"io"

	"github.com/minio/minio-go/v7"
)

type byteRange struct {
	start int64
	end   int64
}

type deliveryStore interface {
	Stat(context.Context, string) (minio.ObjectInfo, error)
	Open(context.Context, string, *byteRange) (io.ReadCloser, error)
}

type minioDeliveryStore struct {
	client *minio.Client
	bucket string
}

func newMinioDeliveryStore(client *minio.Client, bucket string) deliveryStore {
	return &minioDeliveryStore{client: client, bucket: bucket}
}

func (s *minioDeliveryStore) Stat(ctx context.Context, objectKey string) (minio.ObjectInfo, error) {
	return s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
}

func (s *minioDeliveryStore) Open(
	ctx context.Context,
	objectKey string,
	requestedRange *byteRange,
) (io.ReadCloser, error) {
	opts := minio.GetObjectOptions{}
	if requestedRange != nil {
		_ = opts.SetRange(requestedRange.start, requestedRange.end)
	}
	return s.client.GetObject(ctx, s.bucket, objectKey, opts)
}
