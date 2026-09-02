package mediaasset

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	mediaassetdomain "github.com/echovisionlab/geul-api/internal/mediaasset"
)

type CleanupStorage struct {
	client *s3.Client
	bucket string
}

var _ mediaassetdomain.CleanupObjectStore = (*CleanupStorage)(nil)

func NewCleanupStorage(client *s3.Client, bucket string) *CleanupStorage {
	return &CleanupStorage{client: client, bucket: strings.TrimSpace(bucket)}
}

func (s *CleanupStorage) ListObjects(ctx context.Context) ([]mediaassetdomain.StoredObject, error) {
	if s == nil || s.client == nil || s.bucket == "" {
		return nil, fmt.Errorf("media asset cleanup storage is not configured")
	}
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket)})
	var objects []mediaassetdomain.StoredObject
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list media objects: %w", err)
		}
		for _, object := range page.Contents {
			if object.Key == nil {
				continue
			}
			modified := time.Time{}
			if object.LastModified != nil {
				modified = object.LastModified.UTC()
			}
			objects = append(objects, mediaassetdomain.StoredObject{Key: *object.Key, LastModified: modified})
		}
	}
	return objects, nil
}

func (s *CleanupStorage) DeleteObject(ctx context.Context, key string) error {
	if s == nil || s.client == nil || s.bucket == "" {
		return fmt.Errorf("media asset cleanup storage is not configured")
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

func (s *CleanupStorage) DeletePrefix(ctx context.Context, prefix string) error {
	if s == nil || s.client == nil || s.bucket == "" {
		return fmt.Errorf("media asset cleanup storage is not configured")
	}
	if strings.TrimSpace(prefix) == "" {
		return nil
	}
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket), Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list media object prefix: %w", err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		objects := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
		for _, object := range page.Contents {
			objects = append(objects, s3types.ObjectIdentifier{Key: object.Key})
		}
		if _, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		}); err != nil {
			return fmt.Errorf("delete media object prefix: %w", err)
		}
	}
	return nil
}
